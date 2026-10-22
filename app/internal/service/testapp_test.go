package service

import (
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/security"
	_ "gitlab.yurtal.tech/company/pocketbase-app-template/artifacts/migrations"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/model"
)

// newTestApp boots a PocketBase app in a temp dir with the project's
// migrations applied.
func newTestApp(t *testing.T) core.App {
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

// newTestUser saves a user with the given email and verified state.
func newTestUser(t *testing.T, app core.App, email string, verified bool) *core.Record {
	t.Helper()
	users, err := app.FindCollectionByNameOrId(model.UsersCollection)
	if err != nil {
		t.Fatal(err)
	}
	user := core.NewRecord(users)
	user.SetEmail(email)
	user.SetPassword(security.RandomString(20))
	user.SetVerified(verified)
	if err := app.Save(user); err != nil {
		t.Fatalf("save user: %v", err)
	}
	return user
}

// newTestOTP stores an OTP for user sent to sentTo and returns it.
func newTestOTP(t *testing.T, app core.App, user *core.Record, sentTo, code string) *core.OTP {
	t.Helper()
	otp := core.NewOTP(app)
	otp.SetCollectionRef(user.Collection().Id)
	otp.SetRecordRef(user.Id)
	otp.SetSentTo(sentTo)
	otp.SetPassword(code)
	if err := app.Save(otp); err != nil {
		t.Fatalf("save otp: %v", err)
	}
	return otp
}

func TestTestAppHasSchema(t *testing.T) {
	app := newTestApp(t)
	for _, name := range []string{model.UsersCollection, model.TokensCollection, model.TunnelsCollection, model.BillingSubscriptionsCollection, model.SharesCollection} {
		if _, err := app.FindCollectionByNameOrId(name); err != nil {
			t.Fatalf("collection %s: %v", name, err)
		}
	}
}
