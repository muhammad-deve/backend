package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/config"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/model"
)

func TestVerifyWebhookSignature(t *testing.T) {
	body := []byte(`{"data":{"id":"1"}}`)
	secret := "test-signing-secret"
	signature := webhookSignature(body, secret)

	if !verifyWebhookSignature(body, signature, secret) {
		t.Fatal("expected valid signature")
	}
	if verifyWebhookSignature([]byte(`{"data":{"id":"2"}}`), signature, secret) {
		t.Fatal("modified body must not pass verification")
	}
	if verifyWebhookSignature(body, "not-hex", secret) {
		t.Fatal("malformed signature must not pass verification")
	}
}

func TestSubscriptionGrantsPro(t *testing.T) {
	now := time.Date(2026, time.September, 12, 12, 0, 0, 0, time.UTC)
	service := &billingService{now: func() time.Time { return now }}

	day := 24 * time.Hour
	tests := []struct {
		name         string
		status       string
		plan         string
		renewsAt     time.Time
		endsAt       time.Time
		wantEntitled bool
	}{
		{name: "active", status: "active", plan: model.PlanProMonthly, wantEntitled: true},
		{name: "trial", status: "on_trial", plan: model.PlanProMonthly, wantEntitled: true},
		{name: "past due during dunning", status: "past_due", plan: model.PlanProYearly, wantEntitled: true},
		{name: "paused within the paid period", status: "paused", plan: model.PlanProMonthly, renewsAt: now.Add(5 * day), wantEntitled: true},
		{name: "paused within the grace", status: "paused", plan: model.PlanProMonthly, renewsAt: now.Add(-2 * day), wantEntitled: true},
		{name: "paused after the grace", status: "paused", plan: model.PlanProMonthly, renewsAt: now.Add(-3*day - time.Second)},
		{name: "paused without dates", status: "paused", plan: model.PlanProMonthly},
		{name: "unpaid within the grace", status: "unpaid", plan: model.PlanProYearly, renewsAt: now.Add(-2 * day), wantEntitled: true},
		{name: "unpaid after the grace", status: "unpaid", plan: model.PlanProYearly, renewsAt: now.Add(-4 * day)},
		{name: "unpaid uses ends_at without renews_at", status: "unpaid", plan: model.PlanProYearly, endsAt: now.Add(-day), wantEntitled: true},
		{name: "cancelled grace period", status: "cancelled", plan: model.PlanProMonthly, endsAt: now.Add(time.Hour), wantEntitled: true},
		{name: "cancelled at end time", status: "cancelled", plan: model.PlanProMonthly, endsAt: now},
		{name: "cancelled and ended", status: "cancelled", plan: model.PlanProMonthly, endsAt: now.Add(-time.Second)},
		{name: "expired", status: "expired", plan: model.PlanProYearly},
		{name: "unrecognized variant", status: "active"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := service.subscriptionGrantsPro(model.BillingSubscriptionRecord{
				Status:   test.status,
				PlanKey:  test.plan,
				RenewsAt: test.renewsAt,
				EndsAt:   test.endsAt,
			}, now)
			if got != test.wantEntitled {
				t.Fatalf("subscriptionGrantsPro() = %v, want %v", got, test.wantEntitled)
			}
		})
	}
}

func TestProcessInvoiceWebhookStoresTransaction(t *testing.T) {
	repo := &fakeBillingRepository{
		users:  map[string]bool{"user_123": true},
		emails: map[string]string{"user_123": "dev@example.com"},
		subscriptions: []model.BillingSubscriptionRecord{{
			UserID: "user_123", ExternalID: "7001", PlanKey: model.PlanProMonthly, Status: "active",
		}},
	}
	cfg := &config.Config{
		LemonSqueezyStoreID:       "470781",
		LemonSqueezyWebhookSecret: "webhook-secret",
		LemonSqueezyTestMode:      true,
		LemonSqueezyTestEmails:    "dev@example.com",
	}
	service := &billingService{cfg: cfg, repo: repo, now: time.Now}
	body := []byte(`{
		"meta":{"event_name":"subscription_payment_success"},
		"data":{"type":"subscription-invoices","id":"invoice_42","attributes":{
			"store_id":470781,"subscription_id":7001,"user_email":"dev@example.com",
			"billing_reason":"renewal","card_brand":"visa","card_last_four":"4242",
			"currency":"USD","status":"paid","total":299,"refunded_amount":0,
			"created_at":"2026-09-12T00:00:00Z","updated_at":"2026-09-12T00:01:00Z",
			"test_mode":true,"urls":{"invoice_url":"https://app.lemonsqueezy.com/my-orders/test"}
		}}
	}`)

	err := service.ProcessWebhook(context.Background(), body, webhookSignature(body, cfg.LemonSqueezyWebhookSecret))
	if err != nil {
		t.Fatalf("ProcessWebhook() error = %v", err)
	}
	if len(repo.transactions) != 1 {
		t.Fatalf("stored %d transactions, want 1", len(repo.transactions))
	}
	stored := repo.transactions[0]
	if stored.UserID != "user_123" || stored.ExternalID != "invoice_42" || stored.ExternalSubscriptionID != "7001" {
		t.Fatalf("unexpected transaction identity: %#v", stored)
	}
	if stored.AmountCents != 299 || stored.Status != "paid" {
		t.Fatalf("unexpected transaction details: %#v", stored)
	}
}

func TestProcessWebhookRejectsSubscriptionOwnerMismatch(t *testing.T) {
	repo := &fakeBillingRepository{
		users: map[string]bool{"user_1": true, "user_2": true},
		subscriptions: []model.BillingSubscriptionRecord{{
			UserID: "user_1", ExternalID: "7001", PlanKey: model.PlanProMonthly, Status: "active",
		}},
	}
	cfg := &config.Config{
		LemonSqueezyStoreID:             "470781",
		LemonSqueezyProMonthlyVariantID: "991",
		LemonSqueezyWebhookSecret:       "webhook-secret",
		LemonSqueezyTestMode:            true,
	}
	service := &billingService{cfg: cfg, repo: repo, now: time.Now}
	body := []byte(`{"meta":{"event_name":"subscription_updated","custom_data":{"user_id":"user_2"}},"data":{"type":"subscriptions","id":"7001","attributes":{"store_id":470781,"variant_id":991,"status":"active","test_mode":true}}}`)

	err := service.ProcessWebhook(context.Background(), body, webhookSignature(body, cfg.LemonSqueezyWebhookSecret))
	if err == nil || err.Error() != "subscription owner mismatch" {
		t.Fatalf("owner mismatch error = %v", err)
	}
	if len(repo.subscriptions) != 1 {
		t.Fatal("owner mismatch must not write a subscription")
	}
}

func TestProcessSubscriptionWebhook(t *testing.T) {
	repo := &fakeBillingRepository{users: map[string]bool{"user_123": true}, emails: map[string]string{"user_123": "dev@example.com"}}
	cfg := &config.Config{
		LemonSqueezyStoreID:             "470781",
		LemonSqueezyProMonthlyVariantID: "991",
		LemonSqueezyWebhookSecret:       "webhook-secret",
		LemonSqueezyTestMode:            true,
		LemonSqueezyTestEmails:          " Other@example.com , DEV@example.com ",
	}
	service := &billingService{cfg: cfg, repo: repo, now: time.Now}
	body := []byte(`{
		"meta":{"event_name":"subscription_created","custom_data":{"user_id":"user_123"}},
		"data":{"type":"subscriptions","id":"7001","attributes":{
			"store_id":470781,"customer_id":10,"order_id":20,"product_id":30,"variant_id":991,
			"product_name":"GoPort Pro","variant_name":"Monthly","user_email":"dev@example.com",
			"status":"active","card_brand":"visa","card_last_four":"4242",
			"renews_at":"2026-10-12T00:00:00Z","ends_at":null,
			"created_at":"2026-09-12T00:00:00Z","updated_at":"2026-09-12T00:01:00Z","test_mode":true
		}}
	}`)

	err := service.ProcessWebhook(context.Background(), body, webhookSignature(body, cfg.LemonSqueezyWebhookSecret))
	if err != nil {
		t.Fatalf("ProcessWebhook() error = %v", err)
	}
	if len(repo.subscriptions) != 1 {
		t.Fatalf("stored %d subscriptions, want 1", len(repo.subscriptions))
	}
	stored := repo.subscriptions[0]
	if stored.UserID != "user_123" || stored.ExternalID != "7001" || stored.PlanKey != model.PlanProMonthly {
		t.Fatalf("unexpected stored subscription: %#v", stored)
	}
	if stored.Status != "active" {
		t.Fatalf("unexpected payment/status fields: %#v", stored)
	}
}

func TestProcessWebhookRejectsWrongModeAndSignature(t *testing.T) {
	repo := &fakeBillingRepository{users: map[string]bool{"user_123": true}}
	cfg := &config.Config{
		LemonSqueezyStoreID:             "470781",
		LemonSqueezyProMonthlyVariantID: "991",
		LemonSqueezyWebhookSecret:       "webhook-secret",
		LemonSqueezyTestMode:            true,
	}
	service := &billingService{cfg: cfg, repo: repo, now: time.Now}
	body := []byte(`{"meta":{"event_name":"subscription_created","custom_data":{"user_id":"user_123"}},"data":{"type":"subscriptions","id":"7001","attributes":{"store_id":470781,"variant_id":991,"status":"active","test_mode":false}}}`)

	if err := service.ProcessWebhook(context.Background(), body, "bad"); !errors.Is(err, ErrInvalidWebhookSignature) {
		t.Fatalf("invalid signature error = %v", err)
	}
	if err := service.ProcessWebhook(context.Background(), body, webhookSignature(body, cfg.LemonSqueezyWebhookSecret)); !errors.Is(err, ErrWebhookModeMismatch) {
		t.Fatalf("wrong mode error = %v", err)
	}
	if len(repo.subscriptions) != 0 {
		t.Fatal("rejected webhook must not write a subscription")
	}
}

func TestLemonSqueezyCreateCheckout(t *testing.T) {
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/checkouts" {
			http.Error(response, "not found", http.StatusNotFound)
			return
		}
		if request.Header.Get("Authorization") != "Bearer api-key" {
			http.Error(response, "unauthorized", http.StatusUnauthorized)
			return
		}
		if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
			http.Error(response, err.Error(), http.StatusBadRequest)
			return
		}
		response.Header().Set("Content-Type", "application/vnd.api+json")
		_, _ = response.Write([]byte(`{"data":{"attributes":{"url":"https://goport.lemonsqueezy.com/checkout/test"}}}`))
	}))
	defer server.Close()

	client := &lemonSqueezyClient{apiKey: "api-key", baseURL: server.URL, http: server.Client()}
	checkoutURL, err := client.CreateCheckout(context.Background(), lemonCheckoutInput{
		StoreID: "470781", VariantID: "991", UserID: "user_123", Email: "dev@example.com",
		Name: "Dev", RedirectURL: "https://goport.uz/dashboard?checkout=success#billing", TestMode: true,
	})
	if err != nil {
		t.Fatalf("CreateCheckout() error = %v", err)
	}
	if checkoutURL != "https://goport.lemonsqueezy.com/checkout/test" {
		t.Fatalf("checkout URL = %q", checkoutURL)
	}

	data := received["data"].(map[string]any)
	attributes := data["attributes"].(map[string]any)
	checkoutData := attributes["checkout_data"].(map[string]any)
	custom := checkoutData["custom"].(map[string]any)
	if custom["user_id"] != "user_123" || checkoutData["email"] != "dev@example.com" {
		t.Fatalf("checkout identity data = %#v", checkoutData)
	}
	if attributes["test_mode"] != true {
		t.Fatal("checkout must be created in test mode")
	}
}

func TestLemonSqueezySubscriptionMutations(t *testing.T) {
	var updatePayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/subscriptions/7001" || request.Header.Get("Authorization") != "Bearer api-key" {
			http.Error(response, "not found", http.StatusNotFound)
			return
		}
		response.Header().Set("Content-Type", "application/vnd.api+json")
		switch request.Method {
		case http.MethodDelete:
			_, _ = response.Write([]byte(`{"data":{"attributes":{"status":"cancelled","ends_at":"2026-10-14T00:00:00Z","test_mode":true}}}`))
		case http.MethodPatch:
			if err := json.NewDecoder(request.Body).Decode(&updatePayload); err != nil {
				http.Error(response, err.Error(), http.StatusBadRequest)
				return
			}
			_, _ = response.Write([]byte(`{"data":{"attributes":{"variant_id":1999,"status":"active","test_mode":true}}}`))
		default:
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	client := &lemonSqueezyClient{apiKey: "api-key", baseURL: server.URL, http: server.Client()}
	cancelled, err := client.CancelSubscription(context.Background(), "7001")
	if err != nil {
		t.Fatalf("CancelSubscription() error = %v", err)
	}
	if cancelled.Status != "cancelled" || cancelled.EndsAt != "2026-10-14T00:00:00Z" {
		t.Fatalf("unexpected cancellation response: %#v", cancelled)
	}

	updated, err := client.UpdateSubscriptionVariant(context.Background(), "7001", "1999")
	if err != nil {
		t.Fatalf("UpdateSubscriptionVariant() error = %v", err)
	}
	if updated.VariantID.String() != "1999" || updated.Status != "active" {
		t.Fatalf("unexpected update response: %#v", updated)
	}
	attributes := updatePayload["data"].(map[string]any)["attributes"].(map[string]any)
	if attributes["variant_id"] != float64(1999) {
		t.Fatalf("variant update payload = %#v", updatePayload)
	}
}

func TestBillingServiceChangesMonthlyToYearly(t *testing.T) {
	now := time.Date(2026, time.September, 14, 12, 0, 0, 0, time.UTC)
	repo := &fakeBillingRepository{emails: map[string]string{"user_123": "dev@example.com"}, subscriptions: []model.BillingSubscriptionRecord{{
		UserID: "user_123", ExternalID: "7001", VariantID: "991", PlanKey: model.PlanProMonthly, Status: "active", TestMode: true,
	}}}
	client := &fakeLemonSqueezyClient{updateAttributes: lemonSubscriptionAttributes{
		VariantID: json.Number("1999"), Status: "active", RenewsAt: "2027-09-14T00:00:00Z", UpdatedAt: "2026-09-14T12:00:00Z", TestMode: true,
	}}
	service := &billingService{
		cfg:  &config.Config{LemonSqueezyKey: "api-key", LemonSqueezyStoreID: "470781", LemonSqueezyAPIURL: "https://api.lemonsqueezy.test", LemonSqueezyProMonthlyVariantID: "991", LemonSqueezyProYearlyVariantID: "1999", LemonSqueezyTestMode: true, LemonSqueezyTestEmails: "dev@example.com"},
		repo: repo, client: client, now: func() time.Time { return now },
	}

	if err := service.ChangeSubscriptionPlan(context.Background(), "user_123", "yearly"); err != nil {
		t.Fatalf("ChangeSubscriptionPlan() error = %v", err)
	}
	if client.updatedSubscriptionID != "7001" || client.updatedVariantID != "1999" {
		t.Fatalf("unexpected Lemon Squeezy plan update: %#v", client)
	}
	updated := repo.subscriptions[len(repo.subscriptions)-1]
	if updated.PlanKey != model.PlanProYearly || updated.Status != "active" || updated.VariantID != "1999" {
		t.Fatalf("unexpected updated subscription: %#v", updated)
	}
}

func TestBillingServiceSchedulesCancellation(t *testing.T) {
	now := time.Date(2026, time.September, 14, 12, 0, 0, 0, time.UTC)
	endsAt := now.Add(30 * 24 * time.Hour)
	repo := &fakeBillingRepository{emails: map[string]string{"user_123": "dev@example.com"}, subscriptions: []model.BillingSubscriptionRecord{{
		UserID: "user_123", ExternalID: "7001", PlanKey: model.PlanProMonthly, Status: "active", TestMode: true,
	}}}
	client := &fakeLemonSqueezyClient{cancelAttributes: lemonSubscriptionAttributes{
		Status: "cancelled", EndsAt: endsAt.Format(time.RFC3339), UpdatedAt: now.Format(time.RFC3339), TestMode: true,
	}}
	service := &billingService{
		cfg:  &config.Config{LemonSqueezyKey: "api-key", LemonSqueezyStoreID: "470781", LemonSqueezyAPIURL: "https://api.lemonsqueezy.test", LemonSqueezyTestMode: true, LemonSqueezyTestEmails: "dev@example.com"},
		repo: repo, client: client, now: func() time.Time { return now },
	}

	if err := service.CancelSubscription(context.Background(), "user_123"); err != nil {
		t.Fatalf("CancelSubscription() error = %v", err)
	}
	if client.cancelledSubscriptionID != "7001" {
		t.Fatalf("cancelled subscription = %q, want 7001", client.cancelledSubscriptionID)
	}
	updated := repo.subscriptions[len(repo.subscriptions)-1]
	if updated.Status != "cancelled" || !updated.EndsAt.Equal(endsAt) {
		t.Fatalf("unexpected cancelled subscription: %#v", updated)
	}
	if !service.subscriptionGrantsPro(updated, now) {
		t.Fatal("cancelled subscription should retain Pro access through its end date")
	}
}

func TestDeduplicateInitialOrderAndInvoice(t *testing.T) {
	subscriptions := []model.BillingSubscriptionRecord{{ExternalID: "sub_1", OrderID: "order_1"}}
	transactions := []model.BillingTransactionRecord{
		{ExternalID: "invoice_1", ExternalType: "subscription-invoice", ExternalSubscriptionID: "sub_1", BillingReason: "initial"},
		{ExternalID: "order_1", ExternalType: "order"},
		{ExternalID: "invoice_2", ExternalType: "subscription-invoice", ExternalSubscriptionID: "sub_1", BillingReason: "renewal"},
	}

	got := deduplicateInitialTransactions(transactions, subscriptions)
	if len(got) != 2 || got[0].ExternalID != "invoice_1" || got[1].ExternalID != "invoice_2" {
		t.Fatalf("deduplicateInitialTransactions() = %#v", got)
	}
}

func webhookSignature(body []byte, secret string) string {
	hash := hmac.New(sha256.New, []byte(secret))
	_, _ = hash.Write(body)
	return hex.EncodeToString(hash.Sum(nil))
}

type fakeBillingRepository struct {
	users         map[string]bool
	emails        map[string]string // user id -> email
	subscriptions []model.BillingSubscriptionRecord
	transactions  []model.BillingTransactionRecord
}

type fakeLemonSqueezyClient struct {
	checkoutURL             string
	checkoutInput           lemonCheckoutInput
	cancelAttributes        lemonSubscriptionAttributes
	updateAttributes        lemonSubscriptionAttributes
	getAttributes           lemonSubscriptionAttributes
	cancelledSubscriptionID string
	updatedSubscriptionID   string
	updatedVariantID        string
	fetchedSubscriptionID   string
}

func (c *fakeLemonSqueezyClient) GetSubscription(_ context.Context, subscriptionID string) (lemonSubscriptionAttributes, error) {
	c.fetchedSubscriptionID = subscriptionID
	return c.getAttributes, nil
}

func (c *fakeLemonSqueezyClient) CreateCheckout(_ context.Context, input lemonCheckoutInput) (string, error) {
	if c.checkoutURL == "" {
		return "", errors.New("unexpected checkout call")
	}
	c.checkoutInput = input
	return c.checkoutURL, nil
}

func (c *fakeLemonSqueezyClient) CancelSubscription(_ context.Context, subscriptionID string) (lemonSubscriptionAttributes, error) {
	c.cancelledSubscriptionID = subscriptionID
	return c.cancelAttributes, nil
}

func (c *fakeLemonSqueezyClient) UpdateSubscriptionVariant(_ context.Context, subscriptionID, variantID string) (lemonSubscriptionAttributes, error) {
	c.updatedSubscriptionID = subscriptionID
	c.updatedVariantID = variantID
	return c.updateAttributes, nil
}

func (r *fakeBillingRepository) UpsertSubscription(subscription model.BillingSubscriptionRecord) error {
	r.subscriptions = append(r.subscriptions, subscription)
	return nil
}

func (r *fakeBillingRepository) ListSubscriptions(userID string) ([]model.BillingSubscriptionRecord, error) {
	return r.subscriptions, nil
}

func (r *fakeBillingRepository) FindSubscriptionByExternalID(externalID string) (*model.BillingSubscriptionRecord, error) {
	for i := range r.subscriptions {
		if r.subscriptions[i].ExternalID == externalID {
			return &r.subscriptions[i], nil
		}
	}
	return nil, sql.ErrNoRows
}

func (r *fakeBillingRepository) UpsertTransaction(transaction model.BillingTransactionRecord) error {
	r.transactions = append(r.transactions, transaction)
	return nil
}

func (r *fakeBillingRepository) ListTransactions(userID string, limit int) ([]model.BillingTransactionRecord, error) {
	return r.transactions, nil
}

func (r *fakeBillingRepository) UserExists(userID string) (bool, error) {
	return r.users[userID], nil
}

func (r *fakeBillingRepository) FindUserIDByEmail(email string) (string, error) {
	for id, known := range r.emails {
		if known == email {
			return id, nil
		}
	}
	return "", sql.ErrNoRows
}

func (r *fakeBillingRepository) UserEmail(userID string) (string, error) {
	if email, ok := r.emails[userID]; ok {
		return email, nil
	}
	return "", sql.ErrNoRows
}

func TestBillingServiceReportsTrialAlreadyUsed(t *testing.T) {
	now := time.Date(2026, time.September, 22, 12, 0, 0, 0, time.UTC)
	repo := &fakeBillingRepository{subscriptions: []model.BillingSubscriptionRecord{{
		UserID:      "user_123",
		ExternalID:  "7001",
		PlanKey:     model.PlanProMonthly,
		Status:      "expired",
		TrialEndsAt: now.Add(-60 * 24 * time.Hour),
		EndsAt:      now.Add(-30 * 24 * time.Hour),
	}}}
	service := &billingService{cfg: &config.Config{}, repo: repo, now: func() time.Time { return now }}

	data, err := service.Get("user_123")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !data.TrialUsed {
		t.Fatal("TrialUsed = false, want true for an account whose subscription carried a trial")
	}
	if data.IsPro {
		t.Fatal("IsPro = true, want false for an expired subscription")
	}
}

func TestBillingServiceMarksZeroAmountRowsAsTrial(t *testing.T) {
	now := time.Date(2026, time.September, 22, 12, 0, 0, 0, time.UTC)
	repo := &fakeBillingRepository{transactions: []model.BillingTransactionRecord{
		{UserID: "user_123", ExternalID: "t1", AmountCents: 0, Currency: "USD", Status: "paid", ChargedAt: now},
		{UserID: "user_123", ExternalID: "t2", AmountCents: 299, Currency: "USD", Status: "paid", ChargedAt: now},
	}}
	service := &billingService{cfg: &config.Config{}, repo: repo, now: func() time.Time { return now }}

	data, err := service.Get("user_123")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if len(data.Transactions) != 2 {
		t.Fatalf("transactions = %d, want 2", len(data.Transactions))
	}
	if data.Transactions[0].Kind != model.BillingTransactionKindTrial {
		t.Fatalf("zero-amount row kind = %q, want %q", data.Transactions[0].Kind, model.BillingTransactionKindTrial)
	}
	if data.Transactions[1].Kind != model.BillingTransactionKindCharge {
		t.Fatalf("paid row kind = %q, want %q", data.Transactions[1].Kind, model.BillingTransactionKindCharge)
	}
}

func TestBillingServicePortalPrefersCustomerPortalURL(t *testing.T) {
	now := time.Date(2026, time.September, 22, 12, 0, 0, 0, time.UTC)
	repo := &fakeBillingRepository{subscriptions: []model.BillingSubscriptionRecord{{
		UserID: "user_123", ExternalID: "7001", PlanKey: model.PlanProMonthly, Status: "active",
	}}}
	client := &fakeLemonSqueezyClient{}
	client.getAttributes.URLs.CustomerPortal = "https://goport.lemonsqueezy.com/billing?s=1"
	client.getAttributes.URLs.UpdatePaymentMethod = "https://goport.lemonsqueezy.com/card?s=2"
	service := &billingService{
		cfg:  &config.Config{LemonSqueezyKey: "api-key", LemonSqueezyStoreID: "470781", LemonSqueezyAPIURL: "https://api.lemonsqueezy.test"},
		repo: repo, client: client, now: func() time.Time { return now },
	}

	portal, err := service.PortalURL(context.Background(), "user_123")
	if err != nil {
		t.Fatalf("PortalURL() error = %v", err)
	}
	if client.fetchedSubscriptionID != "7001" {
		t.Fatalf("fetched subscription = %q, want 7001", client.fetchedSubscriptionID)
	}
	if portal.URL != "https://goport.lemonsqueezy.com/billing?s=1" {
		t.Fatalf("portal URL = %q, want the customer portal link", portal.URL)
	}
}

func TestBillingServicePortalFallsBackToPaymentMethodURL(t *testing.T) {
	now := time.Date(2026, time.September, 22, 12, 0, 0, 0, time.UTC)
	repo := &fakeBillingRepository{subscriptions: []model.BillingSubscriptionRecord{{
		UserID: "user_123", ExternalID: "7001", PlanKey: model.PlanProMonthly, Status: "active",
	}}}
	client := &fakeLemonSqueezyClient{}
	client.getAttributes.URLs.UpdatePaymentMethod = "https://goport.lemonsqueezy.com/card?s=2"
	service := &billingService{
		cfg:  &config.Config{LemonSqueezyKey: "api-key", LemonSqueezyStoreID: "470781", LemonSqueezyAPIURL: "https://api.lemonsqueezy.test"},
		repo: repo, client: client, now: func() time.Time { return now },
	}

	portal, err := service.PortalURL(context.Background(), "user_123")
	if err != nil {
		t.Fatalf("PortalURL() error = %v", err)
	}
	if portal.URL != "https://goport.lemonsqueezy.com/card?s=2" {
		t.Fatalf("portal URL = %q, want the payment-method link", portal.URL)
	}
}

func TestBillingServicePortalRequiresSubscription(t *testing.T) {
	now := time.Date(2026, time.September, 22, 12, 0, 0, 0, time.UTC)
	service := &billingService{
		cfg:  &config.Config{LemonSqueezyKey: "api-key", LemonSqueezyStoreID: "470781", LemonSqueezyAPIURL: "https://api.lemonsqueezy.test"},
		repo: &fakeBillingRepository{}, client: &fakeLemonSqueezyClient{}, now: func() time.Time { return now },
	}

	if _, err := service.PortalURL(context.Background(), "user_123"); !errors.Is(err, ErrNoSubscription) {
		t.Fatalf("PortalURL() error = %v, want ErrNoSubscription", err)
	}
}

func testModeBilling(repo *fakeBillingRepository, client *fakeLemonSqueezyClient) *billingService {
	return &billingService{
		cfg: &config.Config{
			LemonSqueezyKey: "api-key", LemonSqueezyStoreID: "470781", LemonSqueezyAPIURL: "https://api.lemonsqueezy.test",
			LemonSqueezyProMonthlyVariantID: "991", LemonSqueezyProYearlyVariantID: "1999",
			LemonSqueezyWebhookSecret: "webhook-secret", LemonSqueezyTestMode: true, LemonSqueezyTestEmails: "tester@example.com",
		},
		repo: repo, client: client, now: time.Now,
	}
}

func checkoutUser(id, email string) *core.Record {
	record := core.NewRecord(core.NewAuthCollection("users"))
	record.Id = id
	record.SetEmail(email)
	return record
}

func TestCreateCheckoutInTestModeOnlyForTesters(t *testing.T) {
	client := &fakeLemonSqueezyClient{checkoutURL: "https://goport.lemonsqueezy.com/checkout/test"}
	service := testModeBilling(&fakeBillingRepository{}, client)

	_, err := service.CreateCheckout(context.Background(), checkoutUser("user_1", "someone@example.com"), "monthly")
	if !errors.Is(err, ErrUpgradesClosed) {
		t.Fatalf("non-tester checkout error = %v, want ErrUpgradesClosed", err)
	}
	response, err := service.CreateCheckout(context.Background(), checkoutUser("user_2", "Tester@Example.com"), "monthly")
	if err != nil {
		t.Fatalf("tester checkout: %v", err)
	}
	if response.URL != client.checkoutURL || !client.checkoutInput.TestMode {
		t.Fatalf("unexpected checkout: %#v %#v", response, client.checkoutInput)
	}
}

func TestTestModeWebhooksSkipNonTesters(t *testing.T) {
	repo := &fakeBillingRepository{
		users:  map[string]bool{"user_1": true, "user_2": true},
		emails: map[string]string{"user_1": "someone@example.com", "user_2": "tester@example.com"},
	}
	service := testModeBilling(repo, &fakeLemonSqueezyClient{})
	send := func(body string) error {
		return service.ProcessWebhook(context.Background(), []byte(body), webhookSignature([]byte(body), "webhook-secret"))
	}

	byID := `{"meta":{"event_name":"subscription_created","custom_data":{"user_id":"user_1"}},"data":{"type":"subscriptions","id":"7001","attributes":{"store_id":470781,"variant_id":991,"status":"active","test_mode":true}}}`
	if err := send(byID); !errors.Is(err, ErrWebhookIgnored) {
		t.Fatalf("custom user id: err = %v, want ErrWebhookIgnored", err)
	}
	byEmail := `{"meta":{"event_name":"subscription_created"},"data":{"type":"subscriptions","id":"7002","attributes":{"store_id":470781,"variant_id":991,"status":"active","user_email":"someone@example.com","test_mode":true}}}`
	if err := send(byEmail); !errors.Is(err, ErrWebhookIgnored) {
		t.Fatalf("email match: err = %v, want ErrWebhookIgnored", err)
	}
	order := `{"meta":{"event_name":"order_created"},"data":{"type":"orders","id":"9001","attributes":{"store_id":470781,"user_email":"someone@example.com","status":"paid","total":299,"test_mode":true,"first_order_item":{"variant_id":991}}}}`
	if err := send(order); !errors.Is(err, ErrWebhookIgnored) {
		t.Fatalf("order: err = %v, want ErrWebhookIgnored", err)
	}
	if len(repo.subscriptions) != 0 || len(repo.transactions) != 0 {
		t.Fatal("ignored events must not be stored")
	}

	tester := `{"meta":{"event_name":"subscription_created","custom_data":{"user_id":"user_2"}},"data":{"type":"subscriptions","id":"7003","attributes":{"store_id":470781,"variant_id":991,"status":"active","test_mode":true}}}`
	if err := send(tester); err != nil {
		t.Fatalf("tester webhook: %v", err)
	}
	if len(repo.subscriptions) != 1 {
		t.Fatal("a tester's subscription must be stored")
	}
}

func TestTestSubscriptionsGiveProOnlyToTestersInTestMode(t *testing.T) {
	sub := model.BillingSubscriptionRecord{
		UserID: "user_1", ExternalID: "7001", PlanKey: model.PlanProMonthly, Status: "active", TestMode: true,
		Created: testModeCutoff,
	}
	repo := &fakeBillingRepository{
		emails:        map[string]string{"user_1": "someone@example.com"},
		subscriptions: []model.BillingSubscriptionRecord{sub},
	}
	service := testModeBilling(repo, &fakeLemonSqueezyClient{})

	plan, err := service.Plan("user_1")
	if err != nil || plan.IsPro {
		t.Fatalf("non-tester with a test subscription: pro=%v err=%v", plan.IsPro, err)
	}
	data, err := service.Get("user_1")
	if err != nil || data.IsPro || data.Subscription != nil {
		t.Fatalf("Get for non-tester: %#v %v", data, err)
	}

	repo.emails["user_1"] = "tester@example.com"
	if plan, _ := service.Plan("user_1"); !plan.IsPro {
		t.Fatal("a tester's test subscription must give Pro in test mode")
	}

	service.cfg.LemonSqueezyTestMode = false
	if plan, _ := service.Plan("user_1"); plan.IsPro {
		t.Fatal("test subscriptions must never give Pro in live mode")
	}
	repo.subscriptions[0].TestMode = false
	if plan, _ := service.Plan("user_1"); !plan.IsPro {
		t.Fatal("a live subscription must give Pro")
	}
}

func TestCheckoutSkipsTrialForReturningCustomers(t *testing.T) {
	trialed := model.BillingSubscriptionRecord{
		UserID: "user_2", ExternalID: "7001", PlanKey: model.PlanProMonthly, Status: "expired",
		TrialEndsAt: time.Now().Add(-60 * 24 * time.Hour), EndsAt: time.Now().Add(-30 * 24 * time.Hour),
	}
	tests := []struct {
		name          string
		subscriptions []model.BillingSubscriptionRecord
		noTrial       string
		want          string
	}{
		{name: "first subscription keeps the trial", want: "991"},
		{name: "returning customer gets no trial", subscriptions: []model.BillingSubscriptionRecord{trialed}, noTrial: "995", want: "995"},
		{name: "no-trial variant not set keeps the old variant", subscriptions: []model.BillingSubscriptionRecord{trialed}, want: "991"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &fakeLemonSqueezyClient{checkoutURL: "https://goport.lemonsqueezy.com/checkout/test"}
			service := testModeBilling(&fakeBillingRepository{subscriptions: test.subscriptions}, client)
			service.cfg.LemonSqueezyProMonthlyNoTrialVariantID = test.noTrial
			if _, err := service.CreateCheckout(context.Background(), checkoutUser("user_2", "tester@example.com"), "monthly"); err != nil {
				t.Fatalf("CreateCheckout: %v", err)
			}
			if client.checkoutInput.VariantID != test.want {
				t.Fatalf("variant = %q, want %q", client.checkoutInput.VariantID, test.want)
			}
		})
	}
}

func TestPlanForNoTrialVariants(t *testing.T) {
	service := &billingService{cfg: &config.Config{
		LemonSqueezyProMonthlyVariantID: "991", LemonSqueezyProYearlyVariantID: "1999",
		LemonSqueezyProMonthlyNoTrialVariantID: "995", LemonSqueezyProYearlyNoTrialVariantID: "1995",
	}}
	for variant, want := range map[string]string{"991": model.PlanProMonthly, "995": model.PlanProMonthly, "1999": model.PlanProYearly, "1995": model.PlanProYearly, "": "", "42": ""} {
		if got := service.planForVariant(variant); got != want {
			t.Errorf("planForVariant(%q) = %q, want %q", variant, got, want)
		}
	}
}

func TestTestSubscriptionsStoredBeforeCutoffKeepPro(t *testing.T) {
	for name, created := range map[string]time.Time{
		"stored before the field existed": {},
		"stored just before the cutoff":   testModeCutoff.Add(-time.Second),
	} {
		t.Run(name, func(t *testing.T) {
			repo := &fakeBillingRepository{
				emails: map[string]string{"user_1": "someone@example.com"},
				subscriptions: []model.BillingSubscriptionRecord{{
					UserID: "user_1", ExternalID: "7001", PlanKey: model.PlanProMonthly, Status: "active", TestMode: true, Created: created,
				}},
			}
			service := testModeBilling(repo, &fakeLemonSqueezyClient{})
			if plan, err := service.Plan("user_1"); err != nil || !plan.IsPro {
				t.Fatalf("test mode: pro=%v err=%v", plan.IsPro, err)
			}
			if data, err := service.Get("user_1"); err != nil || data.Subscription == nil {
				t.Fatalf("Get must show the subscription: %#v %v", data, err)
			}
			service.cfg.LemonSqueezyTestMode = false
			if plan, _ := service.Plan("user_1"); !plan.IsPro {
				t.Fatal("live mode must keep it too")
			}
		})
	}
}

func TestTestModeWebhooksKeepEarlierSubscriptionsCurrent(t *testing.T) {
	repo := &fakeBillingRepository{
		users:  map[string]bool{"user_1": true},
		emails: map[string]string{"user_1": "someone@example.com"},
		subscriptions: []model.BillingSubscriptionRecord{
			{UserID: "user_1", ExternalID: "7001", OrderID: "9001", PlanKey: model.PlanProMonthly, Status: "active", TestMode: true},
			{UserID: "user_1", ExternalID: "7002", OrderID: "9002", PlanKey: model.PlanProMonthly, Status: "active", TestMode: true, Created: testModeCutoff.Add(time.Hour)},
		},
	}
	service := testModeBilling(repo, &fakeLemonSqueezyClient{})
	send := func(body string) error {
		return service.ProcessWebhook(context.Background(), []byte(body), webhookSignature([]byte(body), "webhook-secret"))
	}

	cancelled := `{"meta":{"event_name":"subscription_cancelled"},"data":{"type":"subscriptions","id":"7001","attributes":{"store_id":470781,"variant_id":991,"status":"cancelled","ends_at":"2026-11-01T00:00:00Z","test_mode":true}}}`
	if err := send(cancelled); err != nil {
		t.Fatalf("update to an earlier subscription: %v", err)
	}
	if last := repo.subscriptions[len(repo.subscriptions)-1]; last.ExternalID != "7001" || last.Status != "cancelled" {
		t.Fatalf("earlier subscription not updated: %#v", last)
	}
	renewal := `{"meta":{"event_name":"subscription_payment_success"},"data":{"type":"subscription-invoices","id":"inv_1","attributes":{"store_id":470781,"subscription_id":7001,"billing_reason":"renewal","status":"paid","total":299,"test_mode":true}}}`
	if err := send(renewal); err != nil {
		t.Fatalf("renewal of an earlier subscription: %v", err)
	}
	refund := `{"meta":{"event_name":"order_refunded"},"data":{"type":"orders","id":"9001","attributes":{"store_id":470781,"user_email":"someone@example.com","status":"refunded","total":299,"test_mode":true,"first_order_item":{"variant_id":991}}}}`
	if err := send(refund); err != nil {
		t.Fatalf("refund of an earlier order: %v", err)
	}
	if len(repo.transactions) != 2 {
		t.Fatalf("stored %d transactions, want 2", len(repo.transactions))
	}

	newer := `{"meta":{"event_name":"subscription_updated"},"data":{"type":"subscriptions","id":"7002","attributes":{"store_id":470781,"variant_id":991,"status":"active","test_mode":true}}}`
	if err := send(newer); !errors.Is(err, ErrWebhookIgnored) {
		t.Fatalf("subscription stored after the cutoff: err = %v, want ErrWebhookIgnored", err)
	}
}
