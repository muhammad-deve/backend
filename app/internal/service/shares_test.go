package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
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
	for _, name := range []string{"vector.svg", "run.exe", "script.js"} {
		if allowedUpload(name) {
			t.Errorf("dangerous upload %q was allowed", name)
		}
	}
	for _, name := range []string{"report.pdf", "archive.zip", "photo.png", "document.docx", "page.html", "old.HTM"} {
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
	decrypted, legacy, err := service.decryptToken(ciphertext)
	if err != nil || decrypted != token || legacy {
		t.Fatalf("decrypt = %q, %v, legacy %v", decrypted, err, legacy)
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
		cfg:  &config.Config{AppURL: "https://goport.test", ShareFreeMaxUploadBytes: 1024, ShareFreeStorageBytes: 32, ShareProStorageBytes: 64, ShareFreeDefaultRetentionHours: 24, ShareFreeMaxRetentionHours: 48, ShareProDefaultRetentionHours: 24, ShareProMaxRetentionHours: 48},
		repo: repo, billing: fakeShareBilling{}, storageDir: storage, cryptKey: [32]byte{9, 8, 7}, uploads: make(chan struct{}, 2),
	}
	created, err := service.Create("owner", "report.pdf", strings.NewReader("%PDF-cloud-file"), time.Now().Add(time.Hour), "strong password", false)
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
	if _, err := service.Public(token); !errors.Is(err, ErrShareRevoked) {
		t.Fatalf("revoked public error = %v", err)
	}
	if err := service.Delete("owner", record.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(storage, record.StorageKey)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stored object still exists: %v", err)
	}
	if _, err := service.Create("owner", "large.zip", strings.NewReader(strings.Repeat("x", 33)), time.Now().Add(time.Hour), "", false); !errors.Is(err, ErrShareQuota) {
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
	value.ID = fmt.Sprintf("id%013d", r.next)
	value.Created = time.Now().Add(time.Duration(r.next) * time.Millisecond)
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
func (r *memoryShares) Update(id string, expires time.Time, password *string, direct *bool) (*model.ShareRecord, error) {
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
	if direct != nil {
		record.Direct = *direct
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
func (r *memoryShares) TotalStorageUsed() (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var total int64
	for _, record := range r.records {
		total += record.Size
	}
	return total, nil
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
func (r *memoryShares) TokenCiphers(afterID string, limit int) ([]model.ShareRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var result []model.ShareRecord
	for _, record := range r.records {
		if record.ID > afterID {
			result = append(result, model.ShareRecord{ID: record.ID, TokenCipher: record.TokenCipher})
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}
func (r *memoryShares) SetTokenCipher(id, previous, next string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if record, ok := r.records[id]; ok && record.TokenCipher == previous {
		record.TokenCipher = next
		r.records[id] = record
	}
	return nil
}
func (r *memoryShares) Expired(before time.Time, limit int) ([]model.ShareRecord, error) {
	return nil, nil
}

type fakeShareBilling struct{ pro bool }

func (fakeShareBilling) Get(string) (*model.BillingData, error) { return nil, nil }
func (b fakeShareBilling) Plan(string) (model.PlanLimits, error) {
	if b.pro {
		return model.ProPlanLimits(), nil
	}
	return model.FreePlanLimits(), nil
}
func (fakeShareBilling) CreateCheckout(context.Context, *core.Record, string) (*model.CheckoutResponse, error) {
	return nil, nil
}
func (fakeShareBilling) ChangeSubscriptionPlan(context.Context, string, string) error { return nil }
func (fakeShareBilling) CancelSubscription(context.Context, string) error             { return nil }
func (fakeShareBilling) PortalURL(context.Context, string) (*model.BillingPortal, error) {
	return nil, nil
}
func (fakeShareBilling) ProcessWebhook(context.Context, []byte, string) error { return nil }

func shareToken(t *testing.T, item *model.ShareItem) string {
	t.Helper()
	parts := strings.Split(strings.TrimPrefix(strings.TrimPrefix(item.URL, "https://goport.test/s/"), "https://goport.test/f/"), "/")
	return parts[0]
}

func TestShareSecretIsGeneratedOnceAndKept(t *testing.T) {
	dir := t.TempDir()
	first, persistent := loadShareSecret("", dir)
	if !persistent || len(first) != 64 {
		t.Fatalf("generated secret = %q, persistent %v", first, persistent)
	}
	if again, _ := loadShareSecret("", dir); again != first {
		t.Fatal("the generated secret changed on the next start")
	}
	info, err := os.Stat(filepath.Join(dir, shareSecretFile))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("secret file mode = %v", info.Mode().Perm())
	}
	if configured, _ := loadShareSecret("configured-secret-with-enough-length!!", dir); configured != "configured-secret-with-enough-length!!" {
		t.Fatalf("SHARE_SIGNING_KEY ignored: %q", configured)
	}
	// The published example value is never used.
	if example, _ := loadShareSecret("replace-with-at-least-32-random-bytes", dir); example != first {
		t.Fatalf("example key was used: %q", example)
	}
	// An empty file, say from a crash, is replaced with a new key.
	empty := t.TempDir()
	if err := os.WriteFile(filepath.Join(empty, shareSecretFile), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if secret, persistent := loadShareSecret("", empty); !persistent || len(secret) != 64 {
		t.Fatalf("empty key file = %q, %v", secret, persistent)
	}
}

func TestLegacyTokenCopiesAreReencrypted(t *testing.T) {
	shares := newTestShares(t, false)
	shares.cryptKey = legacyShareCryptKeys[0]
	old, err := shares.Create("owner", "notes.txt", strings.NewReader("hello"), time.Time{}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	token := shareToken(t, old)

	shares.cryptKey = sha256.Sum256([]byte("a new private key"))
	shares.persistentKey = true
	stored := shares.repo.(*memoryShares).records[old.ID].TokenCipher
	if plain, legacy, err := shares.decryptToken(stored); err != nil || !legacy || plain != token {
		t.Fatalf("legacy decrypt = %q, %v, %v", plain, legacy, err)
	}
	shares.reencryptLegacyTokens()
	stored = shares.repo.(*memoryShares).records[old.ID].TokenCipher
	if _, err := openToken(legacyShareCryptKeys[0], stored); err == nil {
		t.Fatal("token copy still opens with the public legacy key")
	}
	if plain, legacy, err := shares.decryptToken(stored); err != nil || legacy || plain != token {
		t.Fatalf("re-encrypted decrypt = %q, %v, %v", plain, legacy, err)
	}
	items, _, _, err := shares.List("owner")
	if err != nil || len(items) != 1 || !strings.HasSuffix(items[0].URL, token) {
		t.Fatalf("list after re-encryption = %#v, %v", items, err)
	}
}

func TestUnlockCookieFollowsThePassword(t *testing.T) {
	shares := newTestShares(t, false)
	item, err := shares.Create("owner", "notes.txt", strings.NewReader("hello"), time.Time{}, "long password", false)
	if err != nil {
		t.Fatal(err)
	}
	token := shareToken(t, item)
	record, err := shares.Public(token)
	if err != nil {
		t.Fatal(err)
	}
	cookie := shares.UnlockCookie(token, record, time.Now().Add(time.Hour))
	if !shares.ValidUnlockCookie(cookie, token, record) {
		t.Fatal("fresh cookie refused")
	}
	if shares.ValidUnlockCookie(cookie, strings.Repeat("A", 43), record) {
		t.Fatal("cookie accepted for another link")
	}
	if shares.ValidUnlockCookie(shares.UnlockCookie(token, record, time.Now().Add(-time.Second)), token, record) {
		t.Fatal("expired cookie accepted")
	}
	other := &sharesService{secret: []byte("another key")}
	if other.ValidUnlockCookie(cookie, token, record) {
		t.Fatal("cookie accepted under another key")
	}
	// A new password ends earlier unlocks.
	password := "another password"
	if _, err := shares.Update("owner", item.ID, record.ExpiresAt, &password, nil); err != nil {
		t.Fatal(err)
	}
	changed, _ := shares.Public(token)
	if shares.ValidUnlockCookie(cookie, token, changed) {
		t.Fatal("cookie survived a password change")
	}
	removed := ""
	if _, err := shares.Update("owner", item.ID, record.ExpiresAt, &removed, nil); err != nil {
		t.Fatal(err)
	}
	open, _ := shares.Public(token)
	if shares.ValidUnlockCookie(shares.UnlockCookie(token, open, time.Now().Add(time.Hour)), token, open) {
		t.Fatal("a share without a password accepted an unlock cookie")
	}
}

func TestSharePasswordsIgnoreSurroundingSpaces(t *testing.T) {
	shares := newTestShares(t, false)
	item, err := shares.Create("owner", "notes.txt", strings.NewReader("hello"), time.Time{}, "  long password  ", false)
	if err != nil {
		t.Fatal(err)
	}
	token := shareToken(t, item)
	for _, attempt := range []string{"  long password  ", "long password", "long password\n"} {
		if _, err := shares.VerifyPassword(token, attempt); err != nil {
			t.Errorf("password %q refused: %v", attempt, err)
		}
	}
	if _, err := shares.VerifyPassword(token, "long  password"); !errors.Is(err, ErrSharePassword) {
		t.Fatalf("different password error = %v", err)
	}
}

func TestValidShareToken(t *testing.T) {
	token, _ := randomURLToken(shareTokenBytes)
	if !ValidShareToken(token) {
		t.Fatalf("generated token %q refused", token)
	}
	for _, bad := range []string{"", token[:42], token + "A", strings.Repeat("A", 42) + "=", strings.Repeat("A", 42) + "/", strings.Repeat("A", 1<<20)} {
		if ValidShareToken(bad) {
			t.Errorf("token of length %d accepted: %.50q", len(bad), bad)
		}
	}
	shares := newTestShares(t, false)
	if _, err := shares.Public(strings.Repeat("A", 1<<20)); !errors.Is(err, ErrShareNotFound) {
		t.Fatalf("huge token error = %v", err)
	}
}

func TestLockedViewHidesTheFile(t *testing.T) {
	record := &model.ShareRecord{ID: "rec1", Filename: "secret plan.pdf", MIMEType: "application/pdf", Size: 1234, Downloads: 7, PasswordHash: "$2a$x", ExpiresAt: time.Now().Add(time.Hour)}
	body, _ := json.Marshal(LockedView(record))
	for _, leak := range []string{"secret plan", "application/pdf", "1234", "rec1", "downloads"} {
		if strings.Contains(string(body), leak) {
			t.Fatalf("locked view %s reveals %q", body, leak)
		}
	}
	if !strings.Contains(string(body), `"locked":true`) || !strings.Contains(string(body), `"passwordProtected":true`) {
		t.Fatalf("locked view = %s", body)
	}
	if view := PublicView(record); view.Filename != record.Filename || view.Locked {
		t.Fatalf("public view = %#v", view)
	}
}

func TestFileTypes(t *testing.T) {
	shares := newTestShares(t, true)
	cases := []struct{ name, body, want string }{
		{"Makefile", "build:\n\tgo build ./...\n", "text/plain"},
		{"LICENSE", "MIT License", "text/plain"},
		{"notes.txt", "<p>First paragraph</p>", "text/plain"},
		{"notes.md", "<!-- comment -->\n# Title", "text/plain"},
		{"snippet.txt", "<script>alert(1)</script>", "text/plain"},
		{"feed.rss", "<?xml version=\"1.0\"?><rss></rss>", "text/plain"},
		{"blob", "\x00\x01\x02\x03binary", "application/octet-stream"},
		{"page.html", "<p>hello</p>", "text/html"},
	}
	for _, test := range cases {
		item, err := shares.Create("owner", test.name, strings.NewReader(test.body), time.Time{}, "", false)
		if err != nil || !strings.HasPrefix(item.MIMEType, test.want) {
			t.Errorf("%s = %#v, %v; want %s", test.name, item, err, test.want)
		}
	}
	for _, name := range []string{
		"setup.exe", "setup.exe.", "setup.exe . ", "run.hta", "doc.scr", "a.vbs", "a.js", "a.jar", "link.lnk", "disk.iso",
		"disk.img", "disk.vhdx", "app.msix", "a.reg", "a.ps1", "a.bat", "a.cmd", "a.com", "a.url", "help.chm", "a.svg",
		"a.svgz", "a.xhtml", "data.xml", "page.mht", "a.msi", "a.dll", "photo‮gpj.scr",
	} {
		if _, err := shares.Create("owner", name, strings.NewReader("MZ data"), time.Time{}, "", false); !errors.Is(err, ErrShareType) {
			t.Errorf("%q error = %v", name, err)
		}
	}
	if got := sanitizeFilename("invoice‮fdp.exe . "); got != "invoicefdp.exe" {
		t.Fatalf("sanitized name = %q", got)
	}
}
