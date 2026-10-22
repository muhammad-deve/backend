package service

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/config"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/model"
)

func newTestShares(t *testing.T, pro bool) *sharesService {
	t.Helper()
	return &sharesService{
		cfg: &config.Config{
			AppURL: "https://goport.test", ShareFreeMaxUploadBytes: 64, ShareProMaxUploadBytes: 1 << 30,
			ShareFreeStorageBytes: 100, ShareProStorageBytes: 1 << 31, ShareTotalStorageBytes: 1 << 40,
			ShareFreeDefaultRetentionHours: 24, ShareFreeMaxRetentionHours: 72, ShareFreeMaxDownloads: 3,
			ShareProDefaultRetentionHours: 168, ShareProMaxRetentionHours: 720,
		},
		repo: &memoryShares{records: make(map[string]model.ShareRecord)}, billing: fakeShareBilling{pro: pro},
		storageDir: t.TempDir(), secret: []byte("test secret"), cryptKey: [32]byte{4, 5, 6}, uploads: make(chan struct{}, 2),
		sessions: make(map[string]*shareUploadSession),
	}
}

func TestChunkedUploadResumesAndCompletes(t *testing.T) {
	shares := newTestShares(t, false)
	body := "%PDF-1.7 chunked upload body"
	session, err := shares.StartUpload("owner", model.StartShareUploadRequest{Filename: "notes.pdf", Size: int64(len(body))}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if received, err := shares.WriteUploadChunk("owner", session.ID, 0, strings.NewReader(body[:10])); err != nil || received != 10 {
		t.Fatalf("first chunk = %d, %v", received, err)
	}
	// A chunk that skips ahead is refused and reports where to resume.
	var offsetErr *UploadOffsetError
	if _, err := shares.WriteUploadChunk("owner", session.ID, 20, strings.NewReader(body[20:])); !errors.As(err, &offsetErr) || offsetErr.Received != 10 {
		t.Fatalf("skipping chunk error = %v", err)
	}
	// Retrying from an earlier offset replaces what came after it.
	if _, err := shares.WriteUploadChunk("owner", session.ID, 5, strings.NewReader("XXXXXXXXXXXXXXX")); err != nil {
		t.Fatal(err)
	}
	if received, err := shares.WriteUploadChunk("owner", session.ID, 5, strings.NewReader(body[5:])); err != nil || received != int64(len(body)) {
		t.Fatalf("retried chunk = %d, %v", received, err)
	}
	if _, err := shares.WriteUploadChunk("intruder", session.ID, 0, strings.NewReader(body)); !errors.Is(err, ErrShareUploadNotFound) {
		t.Fatalf("other user's chunk error = %v", err)
	}
	item, err := shares.CompleteUpload("owner", session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(item.URL, "/s/") || item.MIMEType != "application/pdf" || item.Size != int64(len(body)) {
		t.Fatalf("completed item = %#v", item)
	}
	file, _, err := shares.Open(item.URL[strings.LastIndex(item.URL, "/")+1:])
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := io.ReadAll(file)
	file.Close()
	if string(stored) != body {
		t.Fatalf("stored body = %q", stored)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(shares.storageDir, ".upload-*")); len(leftovers) != 0 {
		t.Fatalf("temporary files left behind: %v", leftovers)
	}
	if _, err := shares.CompleteUpload("owner", session.ID); !errors.Is(err, ErrShareUploadNotFound) {
		t.Fatalf("second completion error = %v", err)
	}
}

func TestChunkedUploadChecksPlanBeforeSendingBytes(t *testing.T) {
	free := newTestShares(t, false)
	if _, err := free.StartUpload("owner", model.StartShareUploadRequest{Filename: "big.zip", Size: 65}, time.Time{}); !errors.Is(err, ErrShareTooLarge) {
		t.Fatalf("over the free file limit error = %v", err)
	}
	if _, err := free.StartUpload("owner", model.StartShareUploadRequest{Filename: "cert.pdf", Size: 10, Direct: true}, time.Time{}); !errors.Is(err, ErrShareDirectPro) {
		t.Fatalf("free direct link error = %v", err)
	}
	if _, err := free.StartUpload("owner", model.StartShareUploadRequest{Filename: "logo.svg", Size: 10}, time.Time{}); !errors.Is(err, ErrShareType) {
		t.Fatalf("svg upload error = %v", err)
	}
	// Uploads in progress count against the quota.
	if _, err := free.StartUpload("owner", model.StartShareUploadRequest{Filename: "a.zip", Size: 60}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, err := free.StartUpload("owner", model.StartShareUploadRequest{Filename: "b.zip", Size: 60}, time.Time{}); !errors.Is(err, ErrShareQuota) {
		t.Fatalf("reserved quota error = %v", err)
	}

	pro := newTestShares(t, true)
	if _, err := pro.StartUpload("owner", model.StartShareUploadRequest{Filename: "big.zip", Size: 500 << 20}, time.Time{}); err != nil {
		t.Fatalf("pro large upload refused: %v", err)
	}
	if _, err := pro.StartUpload("owner", model.StartShareUploadRequest{Filename: "cert.pdf", Size: 10, Direct: true, Password: "long password"}, time.Time{}); !errors.Is(err, ErrShareDirectPassword) {
		t.Fatalf("direct link with password error = %v", err)
	}
}

func TestChunkedUploadStoresDisguisedPagesAsText(t *testing.T) {
	shares := newTestShares(t, false)
	body := "<!DOCTYPE html><html><script>alert(1)</script></html>"
	session, err := shares.StartUpload("owner", model.StartShareUploadRequest{Filename: "invoice.pdf", Size: int64(len(body))}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := shares.WriteUploadChunk("owner", session.ID, 0, strings.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	item, err := shares.CompleteUpload("owner", session.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Served as text with nosniff, a page under another name never renders.
	if IsHTMLType(item.MIMEType) || !strings.HasPrefix(item.MIMEType, "text/plain") {
		t.Fatalf("html disguised as pdf stored as %q", item.MIMEType)
	}
}

func TestDirectLinksArePro(t *testing.T) {
	pro := newTestShares(t, true)
	item, err := pro.Create("owner", "My Certificate — 2026.pdf", strings.NewReader("%PDF-1.7 cert"), time.Time{}, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if !item.Direct || !strings.HasPrefix(item.URL, "https://goport.test/f/") || !strings.HasSuffix(item.URL, "/My-Certificate-2026.pdf") {
		t.Fatalf("direct item = %#v", item)
	}
	token := strings.Split(strings.TrimPrefix(item.URL, "https://goport.test/f/"), "/")[0]
	record, err := pro.Public(token)
	if err != nil || !pro.DirectAllowed(record) {
		t.Fatalf("direct link not served: %v", err)
	}
	// Adding a password to a direct link is refused.
	password := "long password"
	if _, err := pro.Update("owner", item.ID, time.Now().Add(time.Hour), &password, nil); !errors.Is(err, ErrShareDirectPassword) {
		t.Fatalf("password on direct link error = %v", err)
	}
	// Turning direct off with the password in the same request works.
	off := false
	updated, err := pro.Update("owner", item.ID, time.Now().Add(time.Hour), &password, &off)
	if err != nil || updated.Direct || !updated.PasswordProtected || !strings.Contains(updated.URL, "/s/") {
		t.Fatalf("switch to share page = %#v, %v", updated, err)
	}

	// When the owner is no longer on Pro the link falls back to the page.
	lapsed := newTestShares(t, true)
	item, _ = lapsed.Create("owner", "cert.pdf", strings.NewReader("%PDF-1.7 cert"), time.Time{}, "", true)
	lapsed.billing = fakeShareBilling{pro: false}
	token = strings.Split(strings.TrimPrefix(item.URL, "https://goport.test/f/"), "/")[0]
	record, _ = lapsed.Public(token)
	if lapsed.DirectAllowed(record) {
		t.Fatal("direct link still served after Pro ended")
	}
	items, _, _, _ := lapsed.List("owner")
	if items[0].Direct || !strings.Contains(items[0].URL, "/s/") {
		t.Fatalf("lapsed list item = %#v", items[0])
	}
	on := true
	if _, err := lapsed.Update("owner", items[0].ID, time.Now().Add(time.Hour), nil, &on); err != nil {
		t.Fatalf("keeping an existing direct flag was refused: %v", err)
	}
}

func TestRestoreRevokedShare(t *testing.T) {
	shares := newTestShares(t, false)
	item, err := shares.Create("owner", "notes.txt", strings.NewReader("hello"), time.Time{}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	token := item.URL[strings.LastIndex(item.URL, "/")+1:]
	if err := shares.Revoke("owner", item.ID); err != nil {
		t.Fatal(err)
	}
	if err := shares.Restore("someone-else", item.ID); !errors.Is(err, ErrShareNotFound) {
		t.Fatalf("cross-owner restore error = %v", err)
	}
	if err := shares.Restore("owner", item.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := shares.Public(token); err != nil {
		t.Fatalf("restored link still unavailable: %v", err)
	}
}

func TestIdleUploadsArePruned(t *testing.T) {
	shares := newTestShares(t, false)
	session, err := shares.StartUpload("owner", model.StartShareUploadRequest{Filename: "a.zip", Size: 10}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	path := shares.sessions[session.ID].tempPath
	shares.sessionsMu.Lock()
	shares.pruneUploadsLocked(time.Now().Add(shareUploadIdleTimeout + time.Minute))
	shares.sessionsMu.Unlock()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("idle upload file kept: %v", err)
	}
	if _, err := shares.WriteUploadChunk("owner", session.ID, 0, strings.NewReader("PK")); !errors.Is(err, ErrShareUploadNotFound) {
		t.Fatalf("pruned upload still writable: %v", err)
	}
}

func TestLinkFilename(t *testing.T) {
	cases := map[string]string{
		"AI kartochka generatori — biznes-reja.pdf": "AI-kartochka-generatori-biznes-reja.pdf",
		"Сертификат.PDF":                            "file.pdf",
		"photo.final.JPEG":                          "photo-final.jpeg",
		"  spaced  out .txt":                        "spaced-out.txt",
	}
	for input, want := range cases {
		if got := linkFilename(input); got != want {
			t.Errorf("linkFilename(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestFreeFilesStopAfterTheirDownloadLimit(t *testing.T) {
	free := newTestShares(t, false)
	item, err := free.Create("owner", "notes.txt", strings.NewReader("hello"), time.Time{}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if item.DownloadLimit != 3 {
		t.Fatalf("download limit = %d, want 3", item.DownloadLimit)
	}
	token := item.URL[strings.LastIndex(item.URL, "/")+1:]
	for range 3 {
		free.CountDownload(item.ID)
	}
	if _, err := free.Public(token); !errors.Is(err, ErrShareDownloadLimit) {
		t.Fatalf("after the limit error = %v", err)
	}

	pro := newTestShares(t, true)
	item, _ = pro.Create("owner", "notes.txt", strings.NewReader("hello"), time.Time{}, "", false)
	token = item.URL[strings.LastIndex(item.URL, "/")+1:]
	for range 10 {
		pro.CountDownload(item.ID)
	}
	if _, err := pro.Public(token); err != nil || item.DownloadLimit != 0 {
		t.Fatalf("pro file limited: %v, limit %d", err, item.DownloadLimit)
	}
}

func TestTotalStorageCapProtectsTheServer(t *testing.T) {
	shares := newTestShares(t, true)
	shares.cfg.ShareTotalStorageBytes = 50
	if _, err := shares.StartUpload("owner", model.StartShareUploadRequest{Filename: "a.zip", Size: 40}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	// The upload in progress counts toward the total for everyone.
	if _, err := shares.StartUpload("someone-else", model.StartShareUploadRequest{Filename: "b.zip", Size: 20}, time.Time{}); !errors.Is(err, ErrShareFull) {
		t.Fatalf("over the total cap error = %v", err)
	}
}

func TestExpiryFollowsThePlan(t *testing.T) {
	free := newTestShares(t, false)
	var expiry *ErrShareExpiry
	if _, err := free.StartUpload("owner", model.StartShareUploadRequest{Filename: "a.pdf", Size: 10}, time.Now().Add(5*24*time.Hour)); !errors.As(err, &expiry) || !strings.Contains(expiry.Message, "3 days") {
		t.Fatalf("free five-day expiry error = %v", err)
	}
	item, err := free.Create("owner", "a.txt", strings.NewReader("hello"), time.Time{}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if expires, _ := time.Parse(time.RFC3339, item.ExpiresAt); expires.Sub(time.Now()) > 25*time.Hour {
		t.Fatalf("free default expiry = %v, want about a day", item.ExpiresAt)
	}

	pro := newTestShares(t, true)
	if _, err := pro.StartUpload("owner", model.StartShareUploadRequest{Filename: "a.pdf", Size: 10}, time.Now().Add(20*24*time.Hour)); err != nil {
		t.Fatalf("pro twenty-day expiry refused: %v", err)
	}
}

func TestHTMLFilesAreAcceptedOnlyUnderHTMLNames(t *testing.T) {
	shares := newTestShares(t, true)
	page := "<!DOCTYPE html><html><body><h1>Report</h1><script>draw()</script></body></html>"
	item, err := shares.Create("owner", "report.html", strings.NewReader(page), time.Time{}, "", false)
	if err != nil {
		t.Fatalf("html upload refused: %v", err)
	}
	if !IsHTMLType(item.MIMEType) || !item.Previewable {
		t.Fatalf("html item = %#v", item)
	}
	// A page that starts with an unusual tag still counts as HTML.
	item, err = shares.Create("owner", "fragment.htm", strings.NewReader("<section>hello</section>"), time.Time{}, "", false)
	if err != nil || !IsHTMLType(item.MIMEType) {
		t.Fatalf("fragment = %#v, %v", item, err)
	}
	// HTML under any other name is kept as plain text, so it never renders
	// outside the sandbox.
	for _, name := range []string{"notes.txt", "invoice.pdf", "photo.png", "README"} {
		item, err := shares.Create("owner", name, strings.NewReader(page), time.Time{}, "", false)
		if err != nil || IsHTMLType(item.MIMEType) || !strings.HasPrefix(item.MIMEType, "text/plain") {
			t.Errorf("html named %s = %#v, %v", name, item, err)
		}
	}
	if _, err := shares.Create("owner", "logo.svg", strings.NewReader("<svg></svg>"), time.Time{}, "", false); !errors.Is(err, ErrShareType) {
		t.Fatalf("svg error = %v", err)
	}
}
