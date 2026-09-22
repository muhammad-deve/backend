package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/config"
)

type lemonSqueezyClientI interface {
	CreateCheckout(ctx context.Context, input lemonCheckoutInput) (string, error)
	CancelSubscription(ctx context.Context, subscriptionID string) (lemonSubscriptionAttributes, error)
	UpdateSubscriptionVariant(ctx context.Context, subscriptionID, variantID string) (lemonSubscriptionAttributes, error)
	GetSubscription(ctx context.Context, subscriptionID string) (lemonSubscriptionAttributes, error)
}

type lemonCheckoutInput struct {
	StoreID     string
	VariantID   string
	UserID      string
	Email       string
	Name        string
	RedirectURL string
	TestMode    bool
}

type lemonSqueezyClient struct {
	apiKey  string
	baseURL string
	http    *http.Client
}

func newLemonSqueezyClient(cfg *config.Config) lemonSqueezyClientI {
	return &lemonSqueezyClient{
		apiKey:  strings.TrimSpace(cfg.LemonSqueezyKey),
		baseURL: strings.TrimRight(cfg.LemonSqueezyAPIURL, "/"),
		http:    &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *lemonSqueezyClient) CreateCheckout(ctx context.Context, input lemonCheckoutInput) (string, error) {
	variantNumber, err := strconv.ParseInt(input.VariantID, 10, 64)
	if err != nil {
		return "", fmt.Errorf("invalid Lemon Squeezy variant id: %w", err)
	}

	payload := map[string]any{
		"data": map[string]any{
			"type": "checkouts",
			"attributes": map[string]any{
				"product_options": map[string]any{
					"redirect_url":     input.RedirectURL,
					"enabled_variants": []int64{variantNumber},
				},
				"checkout_options": map[string]any{
					"embed": false,
				},
				"checkout_data": map[string]any{
					"email": input.Email,
					"name":  input.Name,
					"custom": map[string]string{
						"user_id": input.UserID,
					},
				},
				"test_mode": input.TestMode,
			},
			"relationships": map[string]any{
				"store":   map[string]any{"data": map[string]string{"type": "stores", "id": input.StoreID}},
				"variant": map[string]any{"data": map[string]string{"type": "variants", "id": input.VariantID}},
			},
		},
	}

	var response struct {
		Data struct {
			Attributes struct {
				URL string `json:"url"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := c.do(ctx, http.MethodPost, "/checkouts", payload, &response); err != nil {
		return "", err
	}
	if !validHTTPSURL(response.Data.Attributes.URL) {
		return "", fmt.Errorf("Lemon Squeezy returned an invalid checkout URL")
	}
	return response.Data.Attributes.URL, nil
}

// GetSubscription reads a subscription back from Lemon Squeezy. The portal and
// payment-method URLs it returns are signed and short-lived, so they are
// fetched at the moment the user asks for them rather than cached.
func (c *lemonSqueezyClient) GetSubscription(ctx context.Context, subscriptionID string) (lemonSubscriptionAttributes, error) {
	var response struct {
		Data struct {
			Attributes lemonSubscriptionAttributes `json:"attributes"`
		} `json:"data"`
	}
	path := "/subscriptions/" + url.PathEscape(subscriptionID)
	if err := c.do(ctx, http.MethodGet, path, nil, &response); err != nil {
		return lemonSubscriptionAttributes{}, err
	}
	return response.Data.Attributes, nil
}

func (c *lemonSqueezyClient) CancelSubscription(ctx context.Context, subscriptionID string) (lemonSubscriptionAttributes, error) {
	var response struct {
		Data struct {
			Attributes lemonSubscriptionAttributes `json:"attributes"`
		} `json:"data"`
	}
	path := "/subscriptions/" + url.PathEscape(subscriptionID)
	if err := c.do(ctx, http.MethodDelete, path, nil, &response); err != nil {
		return lemonSubscriptionAttributes{}, err
	}
	if strings.ToLower(strings.TrimSpace(response.Data.Attributes.Status)) != "cancelled" {
		return lemonSubscriptionAttributes{}, fmt.Errorf("Lemon Squeezy did not confirm subscription cancellation")
	}
	return response.Data.Attributes, nil
}

func (c *lemonSqueezyClient) UpdateSubscriptionVariant(ctx context.Context, subscriptionID, variantID string) (lemonSubscriptionAttributes, error) {
	variantNumber, err := strconv.ParseInt(variantID, 10, 64)
	if err != nil {
		return lemonSubscriptionAttributes{}, fmt.Errorf("invalid Lemon Squeezy variant id: %w", err)
	}
	payload := map[string]any{
		"data": map[string]any{
			"type": "subscriptions",
			"id":   subscriptionID,
			"attributes": map[string]any{
				"variant_id": variantNumber,
			},
		},
	}
	var response struct {
		Data struct {
			Attributes lemonSubscriptionAttributes `json:"attributes"`
		} `json:"data"`
	}
	path := "/subscriptions/" + url.PathEscape(subscriptionID)
	if err := c.do(ctx, http.MethodPatch, path, payload, &response); err != nil {
		return lemonSubscriptionAttributes{}, err
	}
	if response.Data.Attributes.VariantID.String() != variantID {
		return lemonSubscriptionAttributes{}, fmt.Errorf("Lemon Squeezy did not confirm the requested subscription variant")
	}
	return response.Data.Attributes, nil
}

func (c *lemonSqueezyClient) do(ctx context.Context, method, path string, body any, target any) error {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(payload)
	}

	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/vnd.api+json")
	request.Header.Set("Content-Type", "application/vnd.api+json")
	request.Header.Set("Authorization", "Bearer "+c.apiKey)

	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("Lemon Squeezy request failed: %w", err)
	}
	defer response.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return fmt.Errorf("read Lemon Squeezy response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return lemonSqueezyAPIError(response.StatusCode, payload)
	}
	if err := json.Unmarshal(payload, target); err != nil {
		return fmt.Errorf("decode Lemon Squeezy response: %w", err)
	}
	return nil
}

func lemonSqueezyAPIError(status int, payload []byte) error {
	var response struct {
		Errors []struct {
			Detail string `json:"detail"`
			Title  string `json:"title"`
		} `json:"errors"`
	}
	if json.Unmarshal(payload, &response) == nil && len(response.Errors) > 0 {
		message := strings.TrimSpace(response.Errors[0].Detail)
		if message == "" {
			message = strings.TrimSpace(response.Errors[0].Title)
		}
		if message != "" {
			return fmt.Errorf("Lemon Squeezy returned %d: %s", status, message)
		}
	}
	return fmt.Errorf("Lemon Squeezy returned HTTP %d", status)
}

func validHTTPSURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme == "https" && parsed.Host != ""
}
