package migrations

import (
	"testing"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/security"
)

func newMigratedApp(t *testing.T) core.App {
	t.Helper()
	app := core.NewBaseApp(core.BaseAppConfig{DataDir: t.TempDir()})
	if err := app.Bootstrap(); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if err := app.RunAppMigrations(); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	t.Cleanup(func() { _ = app.ResetBootstrapState() })
	return app
}

func saveUser(t *testing.T, app core.App, email string, verified bool) *core.Record {
	t.Helper()
	users, _ := app.FindCollectionByNameOrId("users")
	user := core.NewRecord(users)
	user.SetEmail(email)
	user.SetPassword(security.RandomString(20))
	user.SetVerified(verified)
	if err := app.Save(user); err != nil {
		t.Fatal(err)
	}
	tokens, _ := app.FindCollectionByNameOrId("tokens")
	token := core.NewRecord(tokens)
	token.Set("user_id", user.Id)
	token.Set("name", "default")
	token.Set("token", "gp_"+security.RandomString(32))
	if err := app.Save(token); err != nil {
		t.Fatal(err)
	}
	return user
}

func saveLink(t *testing.T, app core.App, user *core.Record, providerID string) {
	t.Helper()
	link := core.NewExternalAuth(app)
	link.SetCollectionRef(user.Collection().Id)
	link.SetRecordRef(user.Id)
	link.SetProvider("google")
	link.SetProviderId(providerID)
	if err := app.Save(link); err != nil {
		t.Fatal(err)
	}
}

func countLinks(t *testing.T, app core.App, user *core.Record) int {
	links, err := app.FindAllExternalAuthsByRecord(user)
	if err != nil {
		t.Fatal(err)
	}
	return len(links)
}

func countTokens(t *testing.T, app core.App, user *core.Record) int {
	tokens, err := app.FindAllRecords("tokens", dbx.HashExp{"user_id": user.Id})
	if err != nil {
		t.Fatal(err)
	}
	return len(tokens)
}

func TestClearPreclaimedAccounts(t *testing.T) {
	app := newMigratedApp(t)
	preclaim := saveUser(t, app, "victim@example.com", false)
	saveLink(t, app, preclaim, "attacker")
	pending := saveUser(t, app, "pending@example.com", false)
	verified := saveUser(t, app, "dev@example.com", true)
	saveLink(t, app, verified, "dev")
	oldKey := preclaim.TokenKey()

	clearPreclaimedAccounts(app)
	clearPreclaimedAccounts(app) // a second run must be harmless

	if countLinks(t, app, preclaim) != 0 || countTokens(t, app, preclaim) != 0 {
		t.Fatal("pre-claimed account must lose its links and tokens")
	}
	reloaded, err := app.FindRecordById("users", preclaim.Id)
	if err != nil {
		t.Fatal("the account itself must stay")
	}
	if reloaded.TokenKey() == oldKey {
		t.Fatal("sessions of the pre-claimed account must end")
	}
	if countTokens(t, app, pending) != 1 {
		t.Fatal("pending sign-ups without links must be left alone")
	}
	if countLinks(t, app, verified) != 1 || countTokens(t, app, verified) != 1 {
		t.Fatal("verified accounts must be left alone")
	}
}

func TestUsersDeleteIsSuperuserOnly(t *testing.T) {
	app := newMigratedApp(t)
	users, err := app.FindCollectionByNameOrId("users")
	if err != nil {
		t.Fatal(err)
	}
	if users.DeleteRule != nil {
		t.Fatalf("users deleteRule = %q, want nil", *users.DeleteRule)
	}
	if users.CreateRule == nil || *users.CreateRule != `@request.context = "oauth2"` {
		t.Fatal("the create rule must stay locked to Google sign-in")
	}
}

func TestAddSubscriptionCreated(t *testing.T) {
	app := newMigratedApp(t)
	subscriptions, err := app.FindCollectionByNameOrId("billing_subscriptions")
	if err != nil {
		t.Fatal(err)
	}
	if field := subscriptions.Fields.GetByName("created"); field == nil {
		t.Fatal("billing_subscriptions must have a created field")
	}

	// Start again from the old schema with rows already stored.
	subscriptions.Fields.RemoveByName("created")
	if err := app.Save(subscriptions); err != nil {
		t.Fatal(err)
	}
	user := saveUser(t, app, "dev@example.com", true)
	saveSubscription := func(externalID, orderID string) {
		t.Helper()
		record := core.NewRecord(subscriptions)
		record.Set("user", user.Id)
		record.Set("external_id", externalID)
		record.Set("order_id", orderID)
		record.Set("status", "active")
		record.Set("test_mode", true)
		if err := app.Save(record); err != nil {
			t.Fatal(err)
		}
	}
	saveSubscription("sub_invoiced", "")
	saveSubscription("sub_ordered", "order_1")
	saveSubscription("sub_unknown", "")
	transactions, _ := app.FindCollectionByNameOrId("billing_transactions")
	saveTransaction := func(externalID, externalType, subscriptionID, chargedAt string) {
		t.Helper()
		record := core.NewRecord(transactions)
		record.Set("user", user.Id)
		record.Set("external_id", externalID)
		record.Set("external_type", externalType)
		record.Set("external_subscription_id", subscriptionID)
		record.Set("status", "paid")
		record.Set("charged_at", chargedAt)
		if err := app.Save(record); err != nil {
			t.Fatal(err)
		}
	}
	saveTransaction("inv_2", "subscription-invoice", "sub_invoiced", "2026-10-24 10:00:00.000Z")
	saveTransaction("inv_1", "subscription-invoice", "sub_invoiced", "2026-10-23 08:00:00.000Z")
	saveTransaction("order_1", "order", "", "2026-10-01 12:00:00.000Z")

	addSubscriptionCreated(app)
	addSubscriptionCreated(app) // a second run must be harmless

	created := func(externalID string) string {
		t.Helper()
		record, err := app.FindFirstRecordByFilter("billing_subscriptions", "external_id = {:id}", dbx.Params{"id": externalID})
		if err != nil {
			t.Fatal(err)
		}
		return record.GetDateTime("created").String()
	}
	if got := created("sub_invoiced"); got != "2026-10-23 08:00:00.000Z" {
		t.Fatalf("invoiced subscription created = %q", got)
	}
	if got := created("sub_ordered"); got != "2026-10-01 12:00:00.000Z" {
		t.Fatalf("ordered subscription created = %q", got)
	}
	if got := created("sub_unknown"); got != "" {
		t.Fatalf("subscription without payments created = %q, want empty", got)
	}

	// New rows get the time they are stored.
	subscriptions, _ = app.FindCollectionByNameOrId("billing_subscriptions")
	saveSubscription("sub_new", "")
	if created("sub_new") == "" {
		t.Fatal("a new row must get a created time")
	}
}
