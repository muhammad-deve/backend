package sqlite

import (
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	_ "gitlab.yurtal.tech/company/pocketbase-app-template/artifacts/migrations"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/model"
)

// migratedApp is a PocketBase app in a temp dir with all of GoPort's
// migrations applied.
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

func newUser(t *testing.T, app core.App, email string) *core.Record {
	t.Helper()
	users, err := app.FindCollectionByNameOrId(model.UsersCollection)
	if err != nil {
		t.Fatal(err)
	}
	user := core.NewRecord(users)
	user.SetEmail(email)
	user.SetPassword("correct horse battery")
	if err := app.Save(user); err != nil {
		t.Fatal(err)
	}
	return user
}

func newTunnel(t *testing.T, app core.App, userID, subdomain string) *core.Record {
	t.Helper()
	tunnels, err := app.FindCollectionByNameOrId(model.TunnelsCollection)
	if err != nil {
		t.Fatal(err)
	}
	tunnel := core.NewRecord(tunnels)
	tunnel.Set("user", userID)
	tunnel.Set("subdomain", subdomain)
	tunnel.Set("local_port", "3000")
	tunnel.Set("protocol", "http")
	if err := app.Save(tunnel); err != nil {
		t.Fatal(err)
	}
	return tunnel
}

func TestMonthlyUsageSurvivesDeletingTunnels(t *testing.T) {
	app := migratedApp(t)
	usage, tunnels := NewUsage(app), NewTunnels(app)
	alice := newUser(t, app, "alice@example.com")
	bob := newUser(t, app, "bob@example.com")
	first := newTunnel(t, app, alice.Id, "alice-one")
	second := newTunnel(t, app, alice.Id, "alice-two")
	bobs := newTunnel(t, app, bob.Id, "bob-one")

	bucket := time.Now().UTC().Truncate(model.UsageBucketDuration)
	for tunnelID, bytes := range map[string]int64{first.Id: 1000, second.Id: 500, bobs.Id: 700} {
		if err := usage.AddTraffic(tunnelID, bucket, model.UsageDelta{Requests: 1, Bytes: bytes}); err != nil {
			t.Fatal(err)
		}
	}
	monthStart := time.Date(bucket.Year(), bucket.Month(), 1, 0, 0, 0, 0, time.UTC)
	used := func(userID string) int64 {
		t.Helper()
		bytes, err := usage.BytesForPeriod(userID, monthStart, time.Now().Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		return bytes
	}
	if used(alice.Id) != 1500 || used(bob.Id) != 700 {
		t.Fatalf("before deleting: alice %d bob %d, want 1500 and 700", used(alice.Id), used(bob.Id))
	}

	// Two deleted tunnels with a bucket at the same time must not collide.
	if err := tunnels.DeleteWithLogs(first.Id); err != nil {
		t.Fatal(err)
	}
	if err := tunnels.DeleteWithLogs(bobs.Id); err != nil {
		t.Fatal(err)
	}
	if used(alice.Id) != 1500 || used(bob.Id) != 700 {
		t.Fatalf("after deleting: alice %d bob %d, want 1500 and 700", used(alice.Id), used(bob.Id))
	}

	// The dashboard chart only lists buckets of tunnels that still exist.
	buckets, err := usage.ListBuckets(alice.Id, monthStart, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 1 || buckets[0].TunnelID != second.Id || buckets[0].Bytes != 500 {
		t.Fatalf("ListBuckets = %+v, want only alice-two's bucket", buckets)
	}

	if _, err := app.FindRecordById(model.TunnelsCollection, first.Id); err == nil {
		t.Fatal("the tunnel row should be gone")
	}
	logs, err := app.FindRecordsByFilter(model.TunnelLogsCollection, "tunnel_id = {:id}", "", 0, 0, dbx.Params{"id": first.Id})
	if err != nil || len(logs) != 0 {
		t.Fatalf("lifetime totals should go with the tunnel: %d rows, %v", len(logs), err)
	}
}

func TestPocketBaseDeleteKeepsUsageToo(t *testing.T) {
	app := migratedApp(t)
	usage := NewUsage(app)
	alice := newUser(t, app, "alice@example.com")
	tunnel := newTunnel(t, app, alice.Id, "alice-one")
	bucket := time.Now().UTC().Truncate(model.UsageBucketDuration)
	if err := usage.AddTraffic(tunnel.Id, bucket, model.UsageDelta{Requests: 1, Bytes: 300}); err != nil {
		t.Fatal(err)
	}
	// An admin deleting the row in the PocketBase UI; tunnel_logs has a
	// required relation, so its row goes first.
	logs, _ := app.FindRecordsByFilter(model.TunnelLogsCollection, "tunnel_id = {:id}", "", 0, 0, dbx.Params{"id": tunnel.Id})
	for _, row := range logs {
		if err := app.Delete(row); err != nil {
			t.Fatal(err)
		}
	}
	if err := app.Delete(tunnel); err != nil {
		t.Fatal(err)
	}
	bytes, err := usage.BytesForPeriod(alice.Id, bucket, bucket.Add(time.Hour))
	if err != nil || bytes != 300 {
		t.Fatalf("BytesForPeriod = %d, %v; want 300", bytes, err)
	}
}
