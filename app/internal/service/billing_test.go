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

	tests := []struct {
		name         string
		status       string
		plan         string
		endsAt       time.Time
		wantEntitled bool
	}{
		{name: "active", status: "active", plan: model.PlanProMonthly, wantEntitled: true},
		{name: "trial", status: "on_trial", plan: model.PlanProMonthly, wantEntitled: true},
		{name: "paused", status: "paused", plan: model.PlanProMonthly, wantEntitled: true},
		{name: "past due during dunning", status: "past_due", plan: model.PlanProYearly, wantEntitled: true},
		{name: "unpaid during dunning", status: "unpaid", plan: model.PlanProYearly, wantEntitled: true},
		{name: "cancelled grace period", status: "cancelled", plan: model.PlanProMonthly, endsAt: now.Add(time.Hour), wantEntitled: true},
		{name: "cancelled at end time", status: "cancelled", plan: model.PlanProMonthly, endsAt: now},
		{name: "cancelled and ended", status: "cancelled", plan: model.PlanProMonthly, endsAt: now.Add(-time.Second)},
		{name: "expired", status: "expired", plan: model.PlanProYearly},
		{name: "unrecognized variant", status: "active"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := service.subscriptionGrantsPro(model.BillingSubscriptionRecord{
				Status:  test.status,
				PlanKey: test.plan,
				EndsAt:  test.endsAt,
			}, now)
			if got != test.wantEntitled {
				t.Fatalf("subscriptionGrantsPro() = %v, want %v", got, test.wantEntitled)
			}
		})
	}
}

func TestProcessInvoiceWebhookStoresTransaction(t *testing.T) {
	repo := &fakeBillingRepository{
		users: map[string]bool{"user_123": true},
		subscriptions: []model.BillingSubscriptionRecord{{
			UserID: "user_123", ExternalID: "7001", PlanKey: model.PlanProMonthly, Status: "active",
		}},
	}
	cfg := &config.Config{
		LemonSqueezyStoreID:       "470781",
		LemonSqueezyWebhookSecret: "webhook-secret",
		LemonSqueezyTestMode:      true,
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
	repo := &fakeBillingRepository{users: map[string]bool{"user_123": true}}
	cfg := &config.Config{
		LemonSqueezyStoreID:             "470781",
		LemonSqueezyProMonthlyVariantID: "991",
		LemonSqueezyWebhookSecret:       "webhook-secret",
		LemonSqueezyTestMode:            true,
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
	subscriptions []model.BillingSubscriptionRecord
	transactions  []model.BillingTransactionRecord
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
	return "", sql.ErrNoRows
}
