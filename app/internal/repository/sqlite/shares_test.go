package sqlite

import (
	"fmt"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/core"
	_ "gitlab.yurtal.tech/company/pocketbase-app-template/artifacts/migrations"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/model"
)

// newShareTestApp runs every migration on an empty database and adds one
// user, who owns sizes as shares.
func newShareTestApp(t *testing.T, sizes ...int64) (*core.BaseApp, *Shares, string) {
	t.Helper()
	app := core.NewBaseApp(core.BaseAppConfig{DataDir: t.TempDir()})
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.ResetBootstrapState() })
	if err := app.RunAllMigrations(); err != nil {
		t.Fatal(err)
	}
	users, err := app.FindCollectionByNameOrId(model.UsersCollection)
	if err != nil {
		t.Fatal(err)
	}
	owner := core.NewRecord(users)
	owner.SetEmail("owner@example.com")
	owner.SetPassword("long password")
	if err := app.Save(owner); err != nil {
		t.Fatal(err)
	}
	repo := NewShares(app)
	for index, size := range sizes {
		if _, err := repo.Create(model.ShareRecord{
			OwnerID: owner.Id, TokenHash: fmt.Sprintf("hash%d", index), TokenCipher: fmt.Sprintf("cipher%d", index),
			StorageKey: fmt.Sprintf("key%d", index), Filename: "a.txt", MIMEType: "text/plain", Size: size,
			ExpiresAt: time.Now().Add(time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
	}
	return app, repo, owner.Id
}

func TestShareTokenCiphersPageAndSwap(t *testing.T) {
	app, repo, _ := newShareTestApp(t, 1, 2, 3)
	// Paging through ciphers visits every row once.
	seen := map[string]string{}
	for after := ""; ; {
		rows, err := repo.TokenCiphers(after, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) == 0 {
			break
		}
		for _, row := range rows {
			seen[row.ID] = row.TokenCipher
			after = row.ID
		}
	}
	if len(seen) != 3 {
		t.Fatalf("paged ciphers = %v", seen)
	}
	for id, previous := range seen {
		// A stale previous value changes nothing.
		if err := repo.SetTokenCipher(id, "stale", "lost"); err != nil {
			t.Fatal(err)
		}
		if err := repo.SetTokenCipher(id, previous, "next"); err != nil {
			t.Fatal(err)
		}
		record, err := app.FindRecordById(model.SharesCollection, id)
		if err != nil || record.GetString("token_cipher") != "next" {
			t.Fatalf("cipher after update = %q, %v", record.GetString("token_cipher"), err)
		}
	}
}
