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
