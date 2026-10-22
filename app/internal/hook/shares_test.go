package hook

import (
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/core"
	_ "gitlab.yurtal.tech/company/pocketbase-app-template/artifacts/migrations"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/model"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/repository/sqlite"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/service"
)

type fakeServices struct {
	service.I
	shares service.SharesI
}

func (f fakeServices) Shares() service.SharesI { return f.shares }

type recordingShares struct {
	service.SharesI
	mu      sync.Mutex
	removed []string
}

func (r *recordingShares) RemoveStoredFile(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.removed = append(r.removed, key)
}

func TestShareFilesAreRemovedWithTheirOwner(t *testing.T) {
	app := core.NewBaseApp(core.BaseAppConfig{DataDir: t.TempDir()})
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	defer app.ResetBootstrapState()
	if err := app.RunAllMigrations(); err != nil {
		t.Fatal(err)
	}
	shares := &recordingShares{}
	New(slog.Default(), fakeServices{shares: shares}).registerShareHooks(app)

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
	repo := sqlite.NewShares(app)
	keys := []string{"aa11", "bb22"}
	for index, key := range keys {
		if _, err := repo.Create(model.ShareRecord{
			OwnerID: owner.Id, TokenHash: key + "hash", TokenCipher: "cipher", StorageKey: key,
			Filename: "notes.txt", MIMEType: "text/plain", Size: int64(index + 1), ExpiresAt: time.Now().Add(time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Deleting the account cascades to its shares, and each one fires the hook.
	if err := app.Delete(owner); err != nil {
		t.Fatal(err)
	}
	shares.mu.Lock()
	defer shares.mu.Unlock()
	if len(shares.removed) != 2 || shares.removed[0] == shares.removed[1] {
		t.Fatalf("removed files = %v, want %v", shares.removed, keys)
	}
	if left, _ := repo.StorageKeys(); len(left) != 0 {
		t.Fatalf("share rows left after the owner was deleted: %v", left)
	}
}
