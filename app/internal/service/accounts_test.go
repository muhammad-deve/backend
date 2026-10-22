package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/model"
)

// accountsTestBilling is a Free-plan BillingI for account tests.
type accountsTestBilling struct{}

func (accountsTestBilling) Get(string) (*model.BillingData, error)               { return nil, nil }
func (accountsTestBilling) Plan(string) (model.PlanLimits, error)                { return model.FreePlanLimits(), nil }
func (accountsTestBilling) ProcessWebhook(context.Context, []byte, string) error { return nil }
func (accountsTestBilling) CreateCheckout(context.Context, *core.Record, string) (*model.CheckoutResponse, error) {
	return nil, nil
}
func (accountsTestBilling) ChangeSubscriptionPlan(context.Context, string, string) error { return nil }
func (accountsTestBilling) CancelSubscription(context.Context, string) error             { return nil }
func (accountsTestBilling) PortalURL(context.Context, string) (*model.BillingPortal, error) {
	return nil, nil
}

// recordingEmail keeps the emails a test sent.
type recordingEmail struct {
	mu   sync.Mutex
	sent []sentEmail
}

type sentEmail struct{ kind, to, name, code string }

func (r *recordingEmail) SendOTP(to, name, code string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, sentEmail{kind: "otp", to: to, name: name, code: code})
	return nil
}

func (r *recordingEmail) last() sentEmail {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.sent) == 0 {
		return sentEmail{}
	}
	return r.sent[len(r.sent)-1]
}

type accountsFixture struct {
	app     core.App
	email   *recordingEmail
	tokens  TokensI
	otp     *otpService
	account *accountService
}

func newAccountsFixture(t *testing.T) *accountsFixture {
	t.Helper()
	app := newTestApp(t)
	email := &recordingEmail{}
	tokens := NewTokensService(app, accountsTestBilling{})
	attempts := newOTPAttempts(maxWrongCodesPerOTP, time.Hour)
	return &accountsFixture{
		app:     app,
		email:   email,
		tokens:  tokens,
		otp:     NewOTPService(app, email, tokens, attempts).(*otpService),
		account: NewAccountService(app, email, tokens, attempts).(*accountService),
	}
}

// linkProvider links a provider account to user, as Google sign-in does.
func linkProvider(t *testing.T, app core.App, user *core.Record, provider, providerID string) {
	t.Helper()
	link := core.NewExternalAuth(app)
	link.SetCollectionRef(user.Collection().Id)
	link.SetRecordRef(user.Id)
	link.SetProvider(provider)
	link.SetProviderId(providerID)
	if err := app.Save(link); err != nil {
		t.Fatalf("save link: %v", err)
	}
}

func userTokens(t *testing.T, app core.App, userID string) []*core.Record {
	t.Helper()
	tokens, err := app.FindAllRecords(model.TokensCollection, dbx.HashExp{"user_id": userID})
	if err != nil {
		t.Fatal(err)
	}
	return tokens
}

func TestCompleteRegistrationReleasesPreclaimedAccount(t *testing.T) {
	f := newAccountsFixture(t)
	// Someone else made the account and linked their Google account to it.
	user := newTestUser(t, f.app, "owner@example.com", false)
	linkProvider(t, f.app, user, "google", "attacker-google-id")
	if err := f.tokens.EnsureDefault(user.Id); err != nil {
		t.Fatal(err)
	}
	oldToken := userTokens(t, f.app, user.Id)[0].GetString("token")
	oldKey := user.TokenKey()

	sent, err := f.otp.SendOTP(&model.SendOTPRequest{Email: "owner@example.com", Name: "Owner"})
	if err != nil {
		t.Fatalf("SendOTP: %v", err)
	}
	if _, err := f.otp.CompleteRegistration(&model.CompleteRegistrationRequest{
		OtpID: sent.OtpID, Code: f.email.last().code, Password: "s3cret-pass",
	}); err != nil {
		t.Fatalf("CompleteRegistration: %v", err)
	}

	user, _ = f.app.FindRecordById(model.UsersCollection, user.Id)
	if !user.Verified() || !user.ValidatePassword("s3cret-pass") {
		t.Fatal("account must be verified with the owner's password")
	}
	if user.TokenKey() == oldKey {
		t.Fatal("old sessions must end")
	}
	links, _ := f.app.FindAllExternalAuthsByRecord(user)
	if len(links) != 0 {
		t.Fatalf("linked sign-ins = %d, want 0", len(links))
	}
	tokens := userTokens(t, f.app, user.Id)
	if len(tokens) != 1 || tokens[0].GetString("token") == oldToken {
		t.Fatalf("want one fresh default token, got %d", len(tokens))
	}
}

func TestReleasePendingKeepsCurrentProvider(t *testing.T) {
	f := newAccountsFixture(t)
	user := newTestUser(t, f.app, "owner@example.com", false)
	linkProvider(t, f.app, user, "github", "someone-else")
	linkProvider(t, f.app, user, "google", "owner-google-id")

	if err := f.account.ReleasePending(user, "google", "owner-google-id"); err != nil {
		t.Fatalf("ReleasePending: %v", err)
	}
	links, _ := f.app.FindAllExternalAuthsByRecord(user)
	if len(links) != 1 || links[0].ProviderId() != "owner-google-id" {
		t.Fatalf("links after release = %d", len(links))
	}
	if len(userTokens(t, f.app, user.Id)) != 1 {
		t.Fatal("a default token must exist after release")
	}
}
