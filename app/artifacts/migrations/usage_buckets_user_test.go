package migrations

import (
	"strings"
	"testing"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

func TestUsageBucketOwnerBackfillAndRerun(t *testing.T) {
	app := core.NewBaseApp(core.BaseAppConfig{DataDir: t.TempDir()})
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.ResetBootstrapState() })
	if err := app.RunAllMigrations(); err != nil {
		t.Fatal(err)
	}

	users, _ := app.FindCollectionByNameOrId("users")
	user := core.NewRecord(users)
	user.SetEmail("owner@example.com")
	user.SetPassword("correct horse battery")
	if err := app.Save(user); err != nil {
		t.Fatal(err)
	}
	tunnels, _ := app.FindCollectionByNameOrId("tunnels")
	owned := core.NewRecord(tunnels)
	owned.Set("user", user.Id)
	owned.Set("subdomain", "owned")
	if err := app.Save(owned); err != nil {
		t.Fatal(err)
	}
	orphan := core.NewRecord(tunnels)
	orphan.Set("subdomain", "orphan")
	if err := app.Save(orphan); err != nil {
		t.Fatal(err)
	}

	// Rows as the old code wrote them, without a user.
	for id, tunnelID := range map[string]string{"bucketowned0001": owned.Id, "bucketorphan001": orphan.Id} {
		_, err := app.DB().NewQuery(`
			INSERT INTO tunnel_usage_buckets (id, tunnel_id, bucket_start, request_count, bytes_transferred, created, updated)
			VALUES ({:id}, {:tunnel}, '2026-10-01 00:00:00.000Z', 1, 100, '2026-10-01 00:00:00.000Z', '2026-10-01 00:00:00.000Z')
		`).Bind(dbx.Params{"id": id, "tunnel": tunnelID}).Execute()
		if err != nil {
			t.Fatal(err)
		}
	}

	// The migration has already run once; running it again must work and
	// fill in the owner.
	if err := addUsageBucketOwner(app); err != nil {
		t.Fatalf("second run: %v", err)
	}
	owner := func(id string) string {
		var row struct {
			User string `db:"user"`
		}
		if err := app.DB().NewQuery("SELECT [[user]] FROM tunnel_usage_buckets WHERE id = {:id}").Bind(dbx.Params{"id": id}).One(&row); err != nil {
			t.Fatal(err)
		}
		return row.User
	}
	if got := owner("bucketowned0001"); got != user.Id {
		t.Fatalf("backfilled user = %q, want %q", got, user.Id)
	}
	if got := owner("bucketorphan001"); got != "" {
		t.Fatalf("a bucket of an ownerless tunnel got user %q", got)
	}

	// Lookups by tunnel and sums by user must be able to use an index.
	// INDEXED BY fails when the named index can't serve the query. (Which
	// plan SQLite picks depends on table size: with two rows a scan wins.)
	for index, where := range map[string]string{
		"idx_tunnel_usage_lookup": "tunnel_id = {:v} AND bucket_start = {:v}",
		"idx_tunnel_usage_user":   "[[user]] = {:v} AND bucket_start >= {:v}",
	} {
		query := "SELECT id FROM tunnel_usage_buckets INDEXED BY " + index + " WHERE " + where
		var plan []struct {
			Detail string `db:"detail"`
		}
		if err := app.DB().NewQuery("EXPLAIN QUERY PLAN " + query).Bind(dbx.Params{"v": "x"}).All(&plan); err != nil {
			t.Errorf("%s: %v", query, err)
			continue
		}
		if len(plan) == 0 || !strings.Contains(plan[0].Detail, index) {
			t.Errorf("%s: plan %+v doesn't use %s", query, plan, index)
		}
	}
}
