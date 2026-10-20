package service

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/config"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/model"
)

func TestShareFilenameAndTypeSafety(t *testing.T) {
	if got := sanitizeFilename(`C:\Users\me\report.pdf`); got != "report.pdf" {
		t.Fatalf("sanitizeFilename = %q", got)
	}
	for _, name := range []string{"payload.html", "vector.svg", "run.exe", "script.js"} {
		if allowedUpload(name) {
			t.Errorf("dangerous upload %q was allowed", name)
		}
	}
	for _, name := range []string{"report.pdf", "archive.zip", "photo.png", "document.docx"} {
		if !allowedUpload(name) {
			t.Errorf("ordinary upload %q was rejected", name)
		}
	}
}

func TestShareTokenEncryptionAndHashing(t *testing.T) {
	service := &sharesService{cryptKey: [32]byte{1, 2, 3, 4}}
	token := strings.Repeat("a", 43)
	ciphertext, err := service.encryptToken(token)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ciphertext, token) {
		t.Fatal("ciphertext contains plaintext token")
	}
	decrypted, err := service.decryptToken(ciphertext)
	if err != nil || decrypted != token {
		t.Fatalf("decrypt = %q, %v", decrypted, err)
	}
	if tokenHash(token) == token || len(tokenHash(token)) != 64 {
		t.Fatal("token hash is not a SHA-256 hex digest")
	}
}

func TestSharePasswordHashing(t *testing.T) {
	hash, err := hashOptionalPassword("strong passphrase")
	if err != nil {
		t.Fatal(err)
	}
	if hash == "strong passphrase" || !strings.HasPrefix(hash, "$2") {
		t.Fatalf("password was not bcrypt hashed: %q", hash)
	}
	if _, err := hashOptionalPassword("short"); err == nil {
		t.Fatal("short password accepted")
	}
}

func TestCloudShareLifecycleOwnershipAndQuota(t *testing.T) {
	storage := t.TempDir()
	repo := &memoryShares{records: make(map[string]model.ShareRecord)}
	service := &sharesService{
		cfg:  &config.Config{AppURL: "https://goport.test", ShareMaxUploadBytes: 1024, ShareFreeStorageBytes: 32, ShareProStorageBytes: 64, ShareDefaultRetentionHours: 24, ShareMaxRetentionHours: 48},
		repo: repo, billing: fakeShareBilling{}, storageDir: storage, cryptKey: [32]byte{9, 8, 7}, uploads: make(chan struct{}, 2),
	}
	created, err := service.Create("owner", "report.pdf", strings.NewReader("%PDF-cloud-file"), time.Now().Add(time.Hour), "strong password")
	if err != nil {
		t.Fatal(err)
	}
	if created.URL == "" || !created.PasswordProtected {
		t.Fatalf("created share = %#v", created)
	}
	token := created.URL[strings.LastIndex(created.URL, "/")+1:]
	if _, err := service.VerifyPassword(token, "wrong password"); !errors.Is(err, ErrSharePassword) {
		t.Fatalf("wrong password error = %v", err)
	}
	if _, err := service.VerifyPassword(token, "strong password"); err != nil {
		t.Fatal(err)
	}
	file, record, err := service.Open(token)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(file)
	file.Close()
	if string(body) != "%PDF-cloud-file" {
		t.Fatalf("stored body = %q", body)
	}
	if err := service.Delete("another-user", record.ID); !errors.Is(err, ErrShareNotFound) {
		t.Fatalf("cross-owner delete error = %v", err)
	}
	if err := service.Revoke("owner", record.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Public(token); !errors.Is(err, ErrShareRevoked) {
		t.Fatalf("revoked public error = %v", err)
	}
	if err := service.Delete("owner", record.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(storage, record.StorageKey)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stored object still exists: %v", err)
	}
	if _, err := service.Create("owner", "large.zip", strings.NewReader(strings.Repeat("x", 33)), time.Now().Add(time.Hour), ""); !errors.Is(err, ErrShareQuota) {
		t.Fatalf("quota error = %v", err)
	}
}

type memoryShares struct {
	mu      sync.Mutex
	next    int
	records map[string]model.ShareRecord
}

func (r *memoryShares) Create(value model.ShareRecord) (*model.ShareRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next++
	value.ID = string(rune('a' + r.next))
	value.Created = time.Now()
	r.records[value.ID] = value
	copy := value
	return &copy, nil
}
func (r *memoryShares) ListOwned(userID string) ([]model.ShareRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var result []model.ShareRecord
	for _, record := range r.records {
		if record.OwnerID == userID {
			result = append(result, record)
		}
	}
	return result, nil
}
func (r *memoryShares) GetOwned(userID, id string) (*model.ShareRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	record, ok := r.records[id]
	if !ok || record.OwnerID != userID {
		return nil, ErrShareNotFound
	}
	copy := record
	return &copy, nil
}
func (r *memoryShares) GetByTokenHash(hash string) (*model.ShareRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, record := range r.records {
		if record.TokenHash == hash {
			copy := record
			return &copy, nil
		}
	}
	return nil, ErrShareNotFound
}
func (r *memoryShares) Update(id string, expires time.Time, password *string) (*model.ShareRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	record, ok := r.records[id]
	if !ok {
		return nil, ErrShareNotFound
	}
	record.ExpiresAt = expires
	if password != nil {
		record.PasswordHash = *password
	}
	r.records[id] = record
	copy := record
	return &copy, nil
}
func (r *memoryShares) SetRevoked(id string, value bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	record, ok := r.records[id]
	if !ok {
		return ErrShareNotFound
	}
	record.Revoked = value
	r.records[id] = record
	return nil
}
func (r *memoryShares) Delete(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.records, id)
	return nil
}
func (r *memoryShares) AddDownload(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	record := r.records[id]
	record.Downloads++
	r.records[id] = record
	return nil
}
func (r *memoryShares) StorageUsed(userID string) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var total int64
	for _, record := range r.records {
		if record.OwnerID == userID {
			total += record.Size
		}
	}
	return total, nil
}
func (r *memoryShares) Expired(before time.Time, limit int) ([]model.ShareRecord, error) {
	return nil, nil
}

type fakeShareBilling struct{}

func (fakeShareBilling) Get(string) (*model.BillingData, error) { return nil, nil }
func (fakeShareBilling) Plan(string) (model.PlanLimits, error)  { return model.FreePlanLimits(), nil }
func (fakeShareBilling) CreateCheckout(context.Context, *core.Record, string) (*model.CheckoutResponse, error) {
	return nil, nil
}
func (fakeShareBilling) ChangeSubscriptionPlan(context.Context, string, string) error { return nil }
func (fakeShareBilling) CancelSubscription(context.Context, string) error             { return nil }
func (fakeShareBilling) PortalURL(context.Context, string) (*model.BillingPortal, error) {
	return nil, nil
}
func (fakeShareBilling) ProcessWebhook(context.Context, []byte, string) error { return nil }
