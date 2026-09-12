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
	CustomerPortal(ctx context.Context, subscriptionID string) (string, error)
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

func (c *lemonSqueezyClient) CustomerPortal(ctx context.Context, subscriptionID string) (string, error) {
	var response struct {
		Data struct {
			Attributes struct {
				URLs struct {
					CustomerPortal string `json:"customer_portal"`
				} `json:"urls"`
			} `json:"attributes"`
		} `json:"data"`
	}
	path := "/subscriptions/" + url.PathEscape(subscriptionID)
	if err := c.do(ctx, http.MethodGet, path, nil, &response); err != nil {
		return "", err
	}
	if !validHTTPSURL(response.Data.Attributes.URLs.CustomerPortal) {
		return "", fmt.Errorf("Lemon Squeezy returned an invalid customer portal URL")
	}
	return response.Data.Attributes.URLs.CustomerPortal, nil
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
