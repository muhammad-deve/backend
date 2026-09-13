package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/config"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/model"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/repository"
)

var (
	ErrBillingNotConfigured    = errors.New("billing is not configured")
	ErrAlreadySubscribed       = errors.New("an active Pro subscription already exists")
	ErrInvalidBillingPlan      = errors.New("invalid billing plan")
	ErrNoSubscription          = errors.New("no subscription found")
	ErrInvalidWebhookSignature = errors.New("invalid webhook signature")
	ErrWebhookModeMismatch     = errors.New("webhook mode does not match the configured Lemon Squeezy mode")
	ErrWebhookStoreMismatch    = errors.New("webhook belongs to a different Lemon Squeezy store")
	ErrWebhookUserNotFound     = errors.New("webhook could not be matched to a GoPort user")
	ErrProRequired             = errors.New("this feature requires an active GoPort Pro subscription")
)

type BillingI interface {
	Get(userID string) (*model.BillingData, error)
	Plan(userID string) (model.PlanLimits, error)
	CreateCheckout(ctx context.Context, user *core.Record, plan string) (*model.CheckoutResponse, error)
	CustomerPortal(ctx context.Context, userID string) (*model.PortalResponse, error)
	ProcessWebhook(ctx context.Context, body []byte, signature string) error
}

type billingService struct {
	cfg    *config.Config
	repo   repository.BillingI
	client lemonSqueezyClientI
	now    func() time.Time
}

func NewBillingService(cfg *config.Config, repo repository.BillingI) BillingI {
	return &billingService{
		cfg:    cfg,
		repo:   repo,
		client: newLemonSqueezyClient(cfg),
		now:    time.Now,
	}
}

func (s *billingService) Plan(userID string) (model.PlanLimits, error) {
	subscriptions, err := s.repo.ListSubscriptions(userID)
	if err != nil {
		return model.PlanLimits{}, err
	}
	for _, subscription := range subscriptions {
		if s.subscriptionGrantsPro(subscription, s.now().UTC()) {
			return model.ProPlanLimits(), nil
		}
	}
	return model.FreePlanLimits(), nil
}

func (s *billingService) CreateCheckout(ctx context.Context, user *core.Record, plan string) (*model.CheckoutResponse, error) {
	if user == nil {
		return nil, fmt.Errorf("missing authenticated user")
	}
	variantID, ok := s.variantForPlan(plan)
	if !ok {
		return nil, ErrInvalidBillingPlan
	}
	if !s.apiConfigured() || variantID == "" {
		return nil, ErrBillingNotConfigured
	}

	limits, err := s.Plan(user.Id)
	if err != nil {
		return nil, err
	}
	if limits.IsPro {
		return nil, ErrAlreadySubscribed
	}

	redirectURL := strings.TrimRight(s.cfg.AppURL, "/") + "/dashboard?checkout=success#billing"
	checkoutURL, err := s.client.CreateCheckout(ctx, lemonCheckoutInput{
		StoreID:     strings.TrimSpace(s.cfg.LemonSqueezyStoreID),
		VariantID:   variantID,
		UserID:      user.Id,
		Email:       user.Email(),
		Name:        user.GetString("name"),
		RedirectURL: redirectURL,
		TestMode:    s.cfg.LemonSqueezyTestMode,
	})
	if err != nil {
		return nil, err
	}
	return &model.CheckoutResponse{URL: checkoutURL}, nil
}

func (s *billingService) CustomerPortal(ctx context.Context, userID string) (*model.PortalResponse, error) {
	if !s.apiConfigured() {
		return nil, ErrBillingNotConfigured
	}
	subscriptions, err := s.repo.ListSubscriptions(userID)
	if err != nil {
		return nil, err
	}
	subscription := s.preferredSubscription(subscriptions)
	if subscription == nil {
		return nil, ErrNoSubscription
	}

	portalURL, err := s.client.CustomerPortal(ctx, subscription.ExternalID)
	if err != nil {
		return nil, err
	}
	return &model.PortalResponse{URL: portalURL}, nil
}

func (s *billingService) Get(userID string) (*model.BillingData, error) {
	subscriptions, err := s.repo.ListSubscriptions(userID)
	if err != nil {
		return nil, err
	}
	transactions, err := s.repo.ListTransactions(userID, 30)
	if err != nil {
		return nil, err
	}

	limits := model.FreePlanLimits()
	for _, subscription := range subscriptions {
		if s.subscriptionGrantsPro(subscription, s.now().UTC()) {
			limits = model.ProPlanLimits()
			break
		}
	}
	availablePlans := make([]string, 0, 2)
	if strings.TrimSpace(s.cfg.LemonSqueezyProMonthlyVariantID) != "" {
		availablePlans = append(availablePlans, "monthly")
	}
	if strings.TrimSpace(s.cfg.LemonSqueezyProYearlyVariantID) != "" {
		availablePlans = append(availablePlans, "yearly")
	}

	data := &model.BillingData{
		IsPro:              limits.IsPro,
		CheckoutConfigured: s.apiConfigured() && len(availablePlans) > 0 && strings.TrimSpace(s.cfg.LemonSqueezyWebhookSecret) != "",
		AvailablePlans:     availablePlans,
		Plan:               limits,
		Transactions:       make([]model.BillingTransaction, 0, len(transactions)),
	}

	selected := s.preferredSubscription(subscriptions)
	if selected != nil {
		amount, interval := s.priceForPlan(selected.PlanKey)
		currency := transactionCurrency(transactions, selected.ExternalID)
		periodEnd := selected.RenewsAt
		if selected.Status == "cancelled" && !selected.EndsAt.IsZero() {
			periodEnd = selected.EndsAt
		}
		data.Subscription = &model.BillingSubscription{
			ID:                selected.ExternalID,
			PlanName:          planDisplayName(selected.PlanKey),
			Status:            selected.Status,
			AmountCents:       amount,
			Currency:          currency,
			Interval:          interval,
			CurrentPeriodEnd:  formatOptionalTime(periodEnd),
			CancelAtPeriodEnd: selected.Status == "cancelled",
			PortalAvailable:   s.apiConfigured(),
		}
	}

	for _, transaction := range deduplicateInitialTransactions(transactions, subscriptions) {
		data.Transactions = append(data.Transactions, model.BillingTransaction{
			ID:          transaction.ID,
			AmountCents: transaction.AmountCents,
			Currency:    normalizedCurrency(transaction.Currency),
			Description: transaction.Description,
			Status:      transaction.Status,
			ChargedAt:   formatOptionalTime(transaction.ChargedAt),
			InvoiceURL:  transaction.InvoiceURL,
		})
	}
	return data, nil
}

func (s *billingService) variantForPlan(plan string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(plan)) {
	case "monthly":
		return strings.TrimSpace(s.cfg.LemonSqueezyProMonthlyVariantID), true
	case "yearly":
		return strings.TrimSpace(s.cfg.LemonSqueezyProYearlyVariantID), true
	default:
		return "", false
	}
}

func (s *billingService) planForVariant(variantID string) string {
	switch strings.TrimSpace(variantID) {
	case strings.TrimSpace(s.cfg.LemonSqueezyProMonthlyVariantID):
		if variantID != "" {
			return model.PlanProMonthly
		}
	case strings.TrimSpace(s.cfg.LemonSqueezyProYearlyVariantID):
		if variantID != "" {
			return model.PlanProYearly
		}
	}
	return ""
}

func (s *billingService) subscriptionGrantsPro(subscription model.BillingSubscriptionRecord, now time.Time) bool {
	if subscription.PlanKey != model.PlanProMonthly && subscription.PlanKey != model.PlanProYearly {
		return false
	}
	switch subscription.Status {
	case "on_trial", "active", "paused", "past_due", "unpaid":
		return true
	case "cancelled":
		return !subscription.EndsAt.IsZero() && now.Before(subscription.EndsAt)
	default:
		return false
	}
}

func (s *billingService) preferredSubscription(subscriptions []model.BillingSubscriptionRecord) *model.BillingSubscriptionRecord {
	now := s.now().UTC()
	for i := range subscriptions {
		if s.subscriptionGrantsPro(subscriptions[i], now) {
			return &subscriptions[i]
		}
	}
	for i := range subscriptions {
		if subscriptions[i].PlanKey == model.PlanProMonthly || subscriptions[i].PlanKey == model.PlanProYearly {
			return &subscriptions[i]
		}
	}
	return nil
}

func (s *billingService) priceForPlan(plan string) (int64, string) {
	if plan == model.PlanProYearly {
		return s.cfg.LemonSqueezyYearlyAmountCents, "year"
	}
	return s.cfg.LemonSqueezyMonthlyAmountCents, "month"
}

func (s *billingService) apiConfigured() bool {
	return strings.TrimSpace(s.cfg.LemonSqueezyKey) != "" &&
		strings.TrimSpace(s.cfg.LemonSqueezyStoreID) != "" &&
		strings.TrimSpace(s.cfg.LemonSqueezyAPIURL) != ""
}

func verifyWebhookSignature(body []byte, signature, secret string) bool {
	provided, err := hex.DecodeString(strings.TrimSpace(signature))
	if err != nil || len(provided) != sha256.Size || secret == "" {
		return false
	}
	hash := hmac.New(sha256.New, []byte(secret))
	_, _ = hash.Write(body)
	return hmac.Equal(hash.Sum(nil), provided)
}

func formatOptionalTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func normalizedCurrency(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	if value == "" {
		return "USD"
	}
	return value
}

func transactionCurrency(transactions []model.BillingTransactionRecord, subscriptionID string) string {
	for _, transaction := range transactions {
		if transaction.ExternalSubscriptionID == subscriptionID && transaction.Currency != "" {
			return normalizedCurrency(transaction.Currency)
		}
	}
	return "USD"
}

func planDisplayName(plan string) string {
	if plan == model.PlanProYearly {
		return "Pro Yearly"
	}
	return "Pro Monthly"
}

func deduplicateInitialTransactions(transactions []model.BillingTransactionRecord, subscriptions []model.BillingSubscriptionRecord) []model.BillingTransactionRecord {
	initialInvoices := make(map[string]bool)
	for _, transaction := range transactions {
		if transaction.ExternalType == "subscription-invoice" && transaction.BillingReason == "initial" {
			initialInvoices[transaction.ExternalSubscriptionID] = true
		}
	}
	orderSubscriptions := make(map[string]string)
	for _, subscription := range subscriptions {
		orderSubscriptions[subscription.OrderID] = subscription.ExternalID
	}

	result := make([]model.BillingTransactionRecord, 0, len(transactions))
	for _, transaction := range transactions {
		if transaction.ExternalType == "order" && initialInvoices[orderSubscriptions[transaction.ExternalID]] {
			continue
		}
		result = append(result, transaction)
	}
	return result
}

func isRecordNotFound(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}
