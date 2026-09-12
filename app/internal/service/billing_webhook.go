package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/model"
)

type lemonWebhook struct {
	Meta struct {
		EventName  string         `json:"event_name"`
		CustomData map[string]any `json:"custom_data"`
	} `json:"meta"`
	Data struct {
		Type       string          `json:"type"`
		ID         string          `json:"id"`
		Attributes json.RawMessage `json:"attributes"`
	} `json:"data"`
}

type lemonSubscriptionAttributes struct {
	StoreID      json.Number `json:"store_id"`
	CustomerID   json.Number `json:"customer_id"`
	OrderID      json.Number `json:"order_id"`
	ProductID    json.Number `json:"product_id"`
	VariantID    json.Number `json:"variant_id"`
	ProductName  string      `json:"product_name"`
	VariantName  string      `json:"variant_name"`
	UserEmail    string      `json:"user_email"`
	Status       string      `json:"status"`
	CardBrand    string      `json:"card_brand"`
	CardLastFour string      `json:"card_last_four"`
	RenewsAt     string      `json:"renews_at"`
	EndsAt       string      `json:"ends_at"`
	TrialEndsAt  string      `json:"trial_ends_at"`
	CreatedAt    string      `json:"created_at"`
	UpdatedAt    string      `json:"updated_at"`
	TestMode     bool        `json:"test_mode"`
}

type lemonInvoiceAttributes struct {
	StoreID        json.Number `json:"store_id"`
	SubscriptionID json.Number `json:"subscription_id"`
	UserEmail      string      `json:"user_email"`
	BillingReason  string      `json:"billing_reason"`
	CardBrand      string      `json:"card_brand"`
	CardLastFour   string      `json:"card_last_four"`
	Currency       string      `json:"currency"`
	Status         string      `json:"status"`
	Total          int64       `json:"total"`
	RefundedAmount int64       `json:"refunded_amount"`
	CreatedAt      string      `json:"created_at"`
	UpdatedAt      string      `json:"updated_at"`
	TestMode       bool        `json:"test_mode"`
	URLs           struct {
		InvoiceURL string `json:"invoice_url"`
	} `json:"urls"`
}

type lemonOrderAttributes struct {
	StoreID        json.Number `json:"store_id"`
	UserEmail      string      `json:"user_email"`
	Currency       string      `json:"currency"`
	Status         string      `json:"status"`
	Total          int64       `json:"total"`
	RefundedAmount int64       `json:"refunded_amount"`
	CreatedAt      string      `json:"created_at"`
	UpdatedAt      string      `json:"updated_at"`
	TestMode       bool        `json:"test_mode"`
	FirstOrderItem struct {
		VariantID   json.Number `json:"variant_id"`
		ProductName string      `json:"product_name"`
		VariantName string      `json:"variant_name"`
	} `json:"first_order_item"`
	URLs struct {
		Receipt string `json:"receipt"`
	} `json:"urls"`
}

func (s *billingService) ProcessWebhook(_ context.Context, body []byte, signature string) error {
	secret := strings.TrimSpace(s.cfg.LemonSqueezyWebhookSecret)
	if secret == "" {
		return ErrBillingNotConfigured
	}
	if !verifyWebhookSignature(body, signature, secret) {
		return ErrInvalidWebhookSignature
	}

	var webhook lemonWebhook
	if err := json.Unmarshal(body, &webhook); err != nil {
		return fmt.Errorf("decode Lemon Squeezy webhook: %w", err)
	}
	if webhook.Data.ID == "" || webhook.Data.Type == "" || webhook.Meta.EventName == "" {
		return fmt.Errorf("invalid Lemon Squeezy webhook payload")
	}

	customUserID := customString(webhook.Meta.CustomData["user_id"])
	switch webhook.Meta.EventName {
	case "subscription_created", "subscription_updated", "subscription_cancelled", "subscription_resumed",
		"subscription_expired", "subscription_paused", "subscription_unpaused", "subscription_plan_changed":
		if webhook.Data.Type != "subscriptions" {
			return fmt.Errorf("unexpected resource type %q for %s", webhook.Data.Type, webhook.Meta.EventName)
		}
		return s.processSubscriptionWebhook(webhook.Data.ID, customUserID, webhook.Data.Attributes)
	case "subscription_payment_success", "subscription_payment_failed", "subscription_payment_recovered", "subscription_payment_refunded":
		if webhook.Data.Type != "subscription-invoices" {
			return fmt.Errorf("unexpected resource type %q for %s", webhook.Data.Type, webhook.Meta.EventName)
		}
		return s.processInvoiceWebhook(webhook.Data.ID, customUserID, webhook.Data.Attributes)
	case "order_created", "order_refunded":
		if webhook.Data.Type != "orders" {
			return fmt.Errorf("unexpected resource type %q for %s", webhook.Data.Type, webhook.Meta.EventName)
		}
		return s.processOrderWebhook(webhook.Data.ID, customUserID, webhook.Data.Attributes)
	default:
		return nil
	}
}

func (s *billingService) processSubscriptionWebhook(externalID, customUserID string, raw json.RawMessage) error {
	var attributes lemonSubscriptionAttributes
	if err := json.Unmarshal(raw, &attributes); err != nil {
		return fmt.Errorf("decode Lemon Squeezy subscription: %w", err)
	}
	if err := s.validateWebhookSource(attributes.StoreID.String(), attributes.TestMode); err != nil {
		return err
	}
	plan := s.planForVariant(attributes.VariantID.String())
	if plan == "" {
		return nil
	}

	userID, err := s.resolveWebhookUser(customUserID, externalID, attributes.UserEmail)
	if err != nil {
		return err
	}
	return s.repo.UpsertSubscription(model.BillingSubscriptionRecord{
		UserID:            userID,
		ExternalID:        externalID,
		CustomerID:        attributes.CustomerID.String(),
		OrderID:           attributes.OrderID.String(),
		ProductID:         attributes.ProductID.String(),
		VariantID:         attributes.VariantID.String(),
		PlanKey:           plan,
		ProductName:       attributes.ProductName,
		VariantName:       attributes.VariantName,
		Status:            strings.ToLower(strings.TrimSpace(attributes.Status)),
		CardBrand:         strings.ToLower(strings.TrimSpace(attributes.CardBrand)),
		CardLastFour:      strings.TrimSpace(attributes.CardLastFour),
		RenewsAt:          parseLemonTime(attributes.RenewsAt),
		EndsAt:            parseLemonTime(attributes.EndsAt),
		TrialEndsAt:       parseLemonTime(attributes.TrialEndsAt),
		ProviderUpdatedAt: firstNonZeroTime(parseLemonTime(attributes.UpdatedAt), parseLemonTime(attributes.CreatedAt)),
		TestMode:          attributes.TestMode,
	})
}

func (s *billingService) processInvoiceWebhook(externalID, customUserID string, raw json.RawMessage) error {
	var attributes lemonInvoiceAttributes
	if err := json.Unmarshal(raw, &attributes); err != nil {
		return fmt.Errorf("decode Lemon Squeezy invoice: %w", err)
	}
	if err := s.validateWebhookSource(attributes.StoreID.String(), attributes.TestMode); err != nil {
		return err
	}
	subscriptionID := attributes.SubscriptionID.String()
	userID, err := s.resolveWebhookUser(customUserID, subscriptionID, attributes.UserEmail)
	if err != nil {
		return err
	}

	description := "GoPort Pro subscription"
	if attributes.BillingReason == "renewal" {
		description = "GoPort Pro renewal"
	} else if attributes.BillingReason == "updated" {
		description = "GoPort Pro plan change"
	}
	return s.repo.UpsertTransaction(model.BillingTransactionRecord{
		UserID:                 userID,
		ExternalID:             externalID,
		ExternalType:           "subscription-invoice",
		ExternalSubscriptionID: subscriptionID,
		BillingReason:          attributes.BillingReason,
		Description:            description,
		AmountCents:            attributes.Total,
		RefundedAmountCents:    attributes.RefundedAmount,
		Currency:               attributes.Currency,
		Status:                 normalizedTransactionStatus(attributes.Status),
		ChargedAt:              parseLemonTime(attributes.CreatedAt),
		CardBrand:              strings.ToLower(strings.TrimSpace(attributes.CardBrand)),
		CardLastFour:           strings.TrimSpace(attributes.CardLastFour),
		InvoiceURL:             attributes.URLs.InvoiceURL,
		ProviderUpdatedAt:      firstNonZeroTime(parseLemonTime(attributes.UpdatedAt), parseLemonTime(attributes.CreatedAt)),
		TestMode:               attributes.TestMode,
	})
}

func (s *billingService) processOrderWebhook(externalID, customUserID string, raw json.RawMessage) error {
	var attributes lemonOrderAttributes
	if err := json.Unmarshal(raw, &attributes); err != nil {
		return fmt.Errorf("decode Lemon Squeezy order: %w", err)
	}
	if err := s.validateWebhookSource(attributes.StoreID.String(), attributes.TestMode); err != nil {
		return err
	}
	if s.planForVariant(attributes.FirstOrderItem.VariantID.String()) == "" {
		return nil
	}
	userID, err := s.resolveWebhookUser(customUserID, "", attributes.UserEmail)
	if err != nil {
		return err
	}

	description := strings.TrimSpace(strings.Join([]string{attributes.FirstOrderItem.ProductName, attributes.FirstOrderItem.VariantName}, " "))
	if description == "" {
		description = "GoPort Pro subscription"
	}
	return s.repo.UpsertTransaction(model.BillingTransactionRecord{
		UserID:              userID,
		ExternalID:          externalID,
		ExternalType:        "order",
		Description:         description,
		AmountCents:         attributes.Total,
		RefundedAmountCents: attributes.RefundedAmount,
		Currency:            attributes.Currency,
		Status:              normalizedTransactionStatus(attributes.Status),
		ChargedAt:           parseLemonTime(attributes.CreatedAt),
		InvoiceURL:          attributes.URLs.Receipt,
		ProviderUpdatedAt:   firstNonZeroTime(parseLemonTime(attributes.UpdatedAt), parseLemonTime(attributes.CreatedAt)),
		TestMode:            attributes.TestMode,
	})
}

func (s *billingService) resolveWebhookUser(customUserID, externalSubscriptionID, email string) (string, error) {
	var existingUserID string
	if externalSubscriptionID != "" {
		subscription, err := s.repo.FindSubscriptionByExternalID(externalSubscriptionID)
		if err == nil {
			existingUserID = subscription.UserID
		} else if !isRecordNotFound(err) {
			return "", err
		}
	}

	if customUserID != "" {
		exists, err := s.repo.UserExists(customUserID)
		if err != nil {
			return "", err
		}
		if !exists {
			return "", ErrWebhookUserNotFound
		}
		if existingUserID != "" && existingUserID != customUserID {
			return "", fmt.Errorf("subscription owner mismatch")
		}
		return customUserID, nil
	}
	if existingUserID != "" {
		return existingUserID, nil
	}

	userID, err := s.repo.FindUserIDByEmail(email)
	if err == nil {
		return userID, nil
	}
	if isRecordNotFound(err) {
		return "", ErrWebhookUserNotFound
	}
	return "", err
}

func (s *billingService) validateWebhookSource(storeID string, testMode bool) error {
	if strings.TrimSpace(storeID) != strings.TrimSpace(s.cfg.LemonSqueezyStoreID) {
		return ErrWebhookStoreMismatch
	}
	if testMode != s.cfg.LemonSqueezyTestMode {
		return ErrWebhookModeMismatch
	}
	return nil
}

func customString(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case float64:
		return strconv.FormatInt(int64(typed), 10)
	case json.Number:
		return typed.String()
	default:
		return ""
	}
}

func parseLemonTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}

func firstNonZeroTime(values ...time.Time) time.Time {
	for _, value := range values {
		if !value.IsZero() {
			return value
		}
	}
	return time.Time{}
}

func normalizedTransactionStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "partial_refund":
		return "partially_refunded"
	case "void", "fraudulent":
		return "failed"
	default:
		return strings.ToLower(strings.TrimSpace(status))
	}
}
