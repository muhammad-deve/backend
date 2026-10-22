package handler

import (
	"net/http"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	_ "gitlab.yurtal.tech/company/pocketbase-app-template/artifacts/migrations"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/model"
)

func TestLogoutEndsExistingSessions(t *testing.T) {
	app := core.NewBaseApp(core.BaseAppConfig{DataDir: t.TempDir()})
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	defer app.ResetBootstrapState()
	if err := app.RunAllMigrations(); err != nil {
		t.Fatal(err)
	}

	users, err := app.FindCollectionByNameOrId(model.UsersCollection)
	if err != nil {
		t.Fatal(err)
	}
	user := core.NewRecord(users)
	user.SetEmail("someone@example.com")
	user.SetPassword("a long enough password")
	user.SetVerified(true)
	if err := app.Save(user); err != nil {
		t.Fatal(err)
	}
	session, err := user.NewAuthToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.FindAuthRecordByToken(session, core.TokenTypeAuth); err != nil {
		t.Fatalf("session should work before logout: %v", err)
	}

	e, rec := testEvent()
	e.App = app
	e.Auth = user
	if err := (&Handler{}).LogoutHandler(e); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if _, err := app.FindAuthRecordByToken(session, core.TokenTypeAuth); err == nil {
		t.Fatal("the old session still works after logout")
	}
}

func TestLogoutNeedsAnAccount(t *testing.T) {
	e, rec := testEvent()
	if err := (&Handler{}).LogoutHandler(e); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}
