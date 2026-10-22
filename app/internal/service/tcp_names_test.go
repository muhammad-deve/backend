package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
	_ "gitlab.yurtal.tech/company/pocketbase-app-template/artifacts/migrations"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/model"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/repository/sqlite"
)

// migratedApp is a PocketBase app in a temp dir with GoPort's migrations.
func migratedApp(t *testing.T) core.App {
	t.Helper()
	app := core.NewBaseApp(core.BaseAppConfig{DataDir: t.TempDir()})
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.ResetBootstrapState() })
	if err := app.RunAllMigrations(); err != nil {
		t.Fatal(err)
	}
	return app
}

// dbService is a tcpService on a real database. Each user in users gets a
// Pro allowance and a token "gp_<user>".
func dbService(t *testing.T, users ...string) (*tcpService, core.App, map[string]string) {
	t.Helper()
	app := migratedApp(t)
	ids := map[string]string{}
	collection, err := app.FindCollectionByNameOrId(model.UsersCollection)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range users {
		user := core.NewRecord(collection)
		user.SetEmail(name + "@example.com")
		user.SetPassword("correct horse battery")
		if err := app.Save(user); err != nil {
			t.Fatal(err)
		}
		ids[name] = user.Id
	}
	tokens := fakeTokens{verify: func(token string) (*model.TokenOwner, error) {
		if id, ok := ids[strings.TrimPrefix(token, "gp_")]; ok {
			return &model.TokenOwner{UserID: id}, nil
		}
		return nil, ErrInvalidToken
	}}
	service := NewTCPService(app, sqlite.NewTunnels(app), sqlite.NewUsage(app), nil, tokens).(*tcpService)
	for _, id := range ids {
		service.quotaCache[id] = trafficQuotaCacheEntry{limits: model.ProPlanLimits(), expiresAt: time.Now().Add(time.Hour)}
	}
	return service, app, ids
}

func saveRow(t *testing.T, app core.App, userID, subdomain string, isCustom bool, lastSaved time.Time) *core.Record {
	t.Helper()
	collection, err := app.FindCollectionByNameOrId(model.TunnelsCollection)
	if err != nil {
		t.Fatal(err)
	}
	record := core.NewRecord(collection)
	record.Set("user", userID)
	record.Set("subdomain", subdomain)
	record.Set("is_custom", isCustom)
	if err := app.Save(record); err != nil {
		t.Fatal(err)
	}
	at := lastSaved.UTC().Format(types.DefaultDateLayout)
	if _, err := app.DB().NewQuery("UPDATE tunnels SET created = {:at}, updated = {:at} WHERE id = {:id}").
		Bind(dbx.Params{"at": at, "id": record.Id}).Execute(); err != nil {
		t.Fatal(err)
	}
	return record
}

func ownerOf(t *testing.T, app core.App, subdomain string) string {
	t.Helper()
	record, err := app.FindFirstRecordByFilter(model.TunnelsCollection, "subdomain = {:s}", dbx.Params{"s": subdomain})
	if err != nil {
		t.Fatal(err)
	}
	return record.GetString("user")
}

func refusalText(err error) string {
	var refused *refusal
	if errors.As(err, &refused) {
		return refused.text
	}
	return ""
}

func TestAnotherAccountsNameIsRefused(t *testing.T) {
	service, app, users := dbService(t, "alice", "bob")
	for _, name := range []struct {
		subdomain string
		custom    bool
		saved     time.Time
	}{
		{"shop", true, time.Now().Add(-24 * time.Hour)},
		{"x7k2p9", false, time.Now().Add(-89 * 24 * time.Hour)},
		{"oldname", true, time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)}, // grandfathered, but used recently below
	} {
		row := saveRow(t, app, users["alice"], name.subdomain, name.custom, name.saved)
		if name.subdomain == "oldname" {
			// Traffic is the latest use.
			logs, _ := app.FindCollectionByNameOrId(model.TunnelLogsCollection)
			activity := core.NewRecord(logs)
			activity.Set("tunnel_id", row.Id)
			activity.Set("last_active", time.Now().Add(-10*24*time.Hour))
			if err := app.Save(activity); err != nil {
				t.Fatal(err)
			}
		}

		_, _, err := service.saveTunnel(name.subdomain, true, users["bob"], "3000", "http")
		if got := refusalText(err); got != nameTakenMessage {
			t.Errorf("%s: err = %v, want %q", name.subdomain, err, nameTakenMessage)
		}
		if owner := ownerOf(t, app, name.subdomain); owner != users["alice"] {
			t.Errorf("%s: owner changed to %s", name.subdomain, owner)
		}
	}
}

func TestLiveNameIsRefusedEvenWhenOld(t *testing.T) {
	service, app, users := dbService(t, "alice", "bob")
	saveRow(t, app, users["alice"], "shop", true, time.Now().Add(-200*24*time.Hour))
	service.sessions["shop"] = nil
	service.owners["shop"] = nil
	service.sessionUsers["shop"] = users["alice"]

	_, _, err := service.saveTunnel("shop", true, users["bob"], "3000", "http")
	if refusalText(err) != nameTakenMessage {
		t.Fatalf("err = %v, want %q", err, nameTakenMessage)
	}
}

func TestNameUnusedFor90DaysCanBeClaimed(t *testing.T) {
	service, app, users := dbService(t, "alice", "bob")
	old := saveRow(t, app, users["alice"], "shop", true, time.Now().Add(-91*24*time.Hour))

	subdomain, tunnelID, err := service.saveTunnel("shop", true, users["bob"], "3000", "http")
	if err != nil {
		t.Fatal(err)
	}
	if subdomain != "shop" || tunnelID == old.Id {
		t.Fatalf("got %s/%s, want shop under a new row", subdomain, tunnelID)
	}
	if owner := ownerOf(t, app, "shop"); owner != users["bob"] {
		t.Fatalf("owner = %s, want bob", owner)
	}
}

func TestOwnNameIsReused(t *testing.T) {
	service, app, users := dbService(t, "alice")
	row := saveRow(t, app, users["alice"], "shop", true, time.Now().Add(-24*time.Hour))
	_, tunnelID, err := service.saveTunnel("shop", true, users["alice"], "4000", "http")
	if err != nil || tunnelID != row.Id {
		t.Fatalf("got %s, %v; want the same row %s", tunnelID, err, row.Id)
	}
}

func TestGeneratedNamesSkipTakenAndReservedNames(t *testing.T) {
	service, app, users := dbService(t, "alice", "bob")
	saveRow(t, app, users["alice"], "taken1", false, time.Now().Add(-365*24*time.Hour))
	draws := []string{"taken1", "status", "fresh1"}
	service.randomName = func() (string, error) {
		next := draws[0]
		draws = draws[1:]
		return next, nil
	}

	subdomain, _, err := service.createRandomTunnel(users["bob"], "3000", "http")
	if err != nil || subdomain != "fresh1" {
		t.Fatalf("got %q, %v; want fresh1", subdomain, err)
	}
	if owner := ownerOf(t, app, "taken1"); owner != users["alice"] {
		t.Fatal("a generated name must never take over an existing row")
	}
}

func TestNamingRules(t *testing.T) {
	for _, name := range []string{"login", "billing", "goport", "abuse", "api", "www"} {
		if err := validateSubdomain(name); err == nil || !strings.Contains(err.Error(), "is reserved") {
			t.Errorf("validateSubdomain(%q) = %v, want reserved", name, err)
		}
	}
	if err := validateSubdomain("xn--80ak6aa92e"); err == nil || !strings.Contains(err.Error(), "subdomain cannot") {
		t.Errorf("punycode name: %v", err)
	}
	if err := validateSubdomain("my-shop"); err != nil {
		t.Errorf("my-shop: %v", err)
	}
	// Saved names are held to the rules they were saved under.
	for name, ok := range map[string]bool{"login": true, "xn--abc": true, "api": false, "www": false, "Bad.Name": false, "": false} {
		if got := validateLegacySubdomain(name) == nil; got != ok {
			t.Errorf("validateLegacySubdomain(%q) ok = %v, want %v", name, got, ok)
		}
	}
}

func TestSavedNamesBreakingTheRulesGetARandomName(t *testing.T) {
	service, app, users := dbService(t, "alice")
	// Written straight through the REST API before it was locked.
	saveRow(t, app, users["alice"], "www", false, time.Now())

	for _, req := range []tunnelRegistrationRequest{
		{Type: "http", Port: "3000", Token: "gp_alice", Resume: "www"},
		{Type: "http", Port: "3000", Token: "gp_alice"},
	} {
		subdomain, _, _, _, _, err := service.resolveTunnel(req)
		if err != nil {
			t.Fatal(err)
		}
		if subdomain == "www" || validateSubdomain(subdomain) != nil {
			t.Fatalf("resume %q got %q, want a fresh valid name", req.Resume, subdomain)
		}
	}
}

func TestOwnerKeepsANameReservedLater(t *testing.T) {
	service, app, users := dbService(t, "alice", "bob")
	saveRow(t, app, users["alice"], "login", true, time.Now())

	subdomain, _, _, _, _, err := service.resolveTunnel(tunnelRegistrationRequest{Type: "http", Port: "3000", Token: "gp_alice", Subdomain: "login"})
	if err != nil || subdomain != "login" {
		t.Fatalf("owner: got %q, %v", subdomain, err)
	}
	_, _, _, _, _, err = service.resolveTunnel(tunnelRegistrationRequest{Type: "http", Port: "3000", Token: "gp_bob", Subdomain: "login"})
	if err == nil || !strings.Contains(err.Error(), "is reserved") {
		t.Fatalf("another account: err = %v, want reserved", err)
	}
}

func TestStopTunnelChecksTheLiveSessionOwner(t *testing.T) {
	service, app, users := dbService(t, "alice", "bob")
	saveRow(t, app, users["alice"], "shop", true, time.Now())
	session, cliEnd := heldSession(t, service, "shop", users["bob"])
	answeringCLI(t, cliEnd)

	if err := service.StopTunnel(users["alice"], "shop"); !errors.Is(err, ErrTunnelInactive) {
		t.Fatalf("err = %v, want ErrTunnelInactive", err)
	}
	if session.IsClosed() {
		t.Fatal("another account's live session must not be closed")
	}
}
