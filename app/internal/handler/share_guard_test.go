package handler

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/config"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/model"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/service"
)

type guardClock struct{ now time.Time }

func (c *guardClock) advance(d time.Duration) { c.now = c.now.Add(d) }

func newTestGuard() (*sharePasswordGuard, *guardClock) {
	clock := &guardClock{now: time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)}
	guard := newSharePasswordGuard()
	guard.now = func() time.Time { return clock.now }
	return guard, clock
}

// wrong makes one wrong attempt and returns the pause it caused.
func wrong(t *testing.T, guard *sharePasswordGuard, link, visitor string) (time.Duration, int) {
	t.Helper()
	if wait, reason := guard.begin(link, visitor); wait > 0 {
		t.Fatalf("attempt refused before it started: %v (%s)", wait, reason)
	}
	wait, _, left := guard.finish(link, visitor, shareAttemptWrong)
	return wait, left
}

func TestSharePasswordGuardEscalates(t *testing.T) {
	guard, clock := newTestGuard()
	steps := []struct {
		pause time.Duration
		left  int
	}{
		{0, 2}, {0, 1}, {time.Minute, 2},
		{0, 1}, {30 * time.Minute, 2},
		{0, 1}, {24 * time.Hour, 1},
	}
	for index, step := range steps {
		pause, left := wrong(t, guard, "link", "1.2.3.4")
		if pause != step.pause || left != step.left {
			t.Fatalf("failure %d: pause %v left %d, want %v and %d", index+1, pause, left, step.pause, step.left)
		}
		if pause > 0 {
			if wait, reason := guard.begin("link", "1.2.3.4"); wait != pause || reason != shareLockVisitor {
				t.Fatalf("failure %d: begin during pause = %v %s", index+1, wait, reason)
			}
			if status := guard.status("link", "1.2.3.4"); status != pause {
				t.Fatalf("failure %d: status = %v", index+1, status)
			}
			clock.advance(pause)
		}
	}
	// Other visitors and other links are unaffected.
	if wait, _ := guard.begin("link", "5.6.7.8"); wait != 0 {
		t.Fatalf("another visitor was paused: %v", wait)
	}
	guard.finish("link", "5.6.7.8", shareAttemptOther)
	if wait, _ := guard.begin("other-link", "1.2.3.4"); wait != 0 {
		t.Fatalf("another link was paused: %v", wait)
	}
}

func TestSharePasswordGuardResetsOnSuccess(t *testing.T) {
	guard, _ := newTestGuard()
	wrong(t, guard, "link", "1.2.3.4")
	wrong(t, guard, "link", "1.2.3.4")
	guard.begin("link", "1.2.3.4")
	guard.finish("link", "1.2.3.4", shareAttemptCorrect)
	if _, left := wrong(t, guard, "link", "1.2.3.4"); left != 2 {
		t.Fatalf("attempts left after a success = %d, want 2", left)
	}
}

func TestSharePasswordGuardForgetsAfterADay(t *testing.T) {
	guard, clock := newTestGuard()
	wrong(t, guard, "link", "1.2.3.4")
	wrong(t, guard, "link", "1.2.3.4")
	clock.advance(25 * time.Hour)
	if pause, left := wrong(t, guard, "link", "1.2.3.4"); pause != 0 || left != 2 {
		t.Fatalf("old failures still counted: pause %v left %d", pause, left)
	}
}

func TestSharePasswordGuardPausesLinkUnderDistributedGuessing(t *testing.T) {
	guard, clock := newTestGuard()
	for index := 0; index < shareLinkMaxFailures; index++ {
		visitor := "10.0." + string(rune('a'+index/26)) + "." + string(rune('a'+index%26))
		wrong(t, guard, "link", visitor)
		clock.advance(time.Second)
	}
	wait, reason := guard.begin("link", "9.9.9.9")
	if reason != shareLockLink || wait <= 0 || wait > shareLinkWindow {
		t.Fatalf("link not paused: %v %s", wait, reason)
	}
	clock.advance(wait)
	if wait, _ := guard.begin("link", "9.9.9.9"); wait != 0 {
		t.Fatalf("link still paused after the window: %v", wait)
	}
}

func TestSharePasswordGuardAllowsOneCheckAtATime(t *testing.T) {
	guard, _ := newTestGuard()
	guard.begin("link", "1.2.3.4")
	if wait, reason := guard.begin("link", "1.2.3.4"); wait == 0 || reason != shareLockBusy {
		t.Fatalf("parallel attempt allowed: %v %s", wait, reason)
	}
	guard.finish("link", "1.2.3.4", shareAttemptOther)
	if wait, _ := guard.begin("link", "1.2.3.4"); wait != 0 {
		t.Fatalf("attempt refused after release: %v", wait)
	}
}

func TestShareVisitorKeyTrustsOnlyTheProxy(t *testing.T) {
	viaProxy := httptest.NewRequest("POST", "/", nil)
	viaProxy.RemoteAddr = "172.17.0.1:50000"
	viaProxy.Header.Set("X-Forwarded-For", "6.6.6.6, 84.54.1.2")
	if got := shareVisitorKey(viaProxy); got != "84.54.1.2" {
		t.Fatalf("proxied visitor = %q, want the address nginx appended", got)
	}
	viaProxy.Header.Set("X-Real-IP", "84.54.1.3")
	if got := shareVisitorKey(viaProxy); got != "84.54.1.3" {
		t.Fatalf("proxied visitor with X-Real-IP = %q", got)
	}

	direct := httptest.NewRequest("POST", "/", nil)
	direct.RemoteAddr = "84.54.1.2:40000"
	direct.Header.Set("X-Real-IP", "1.1.1.1")
	direct.Header.Set("X-Forwarded-For", "1.1.1.1")
	if got := shareVisitorKey(direct); got != "84.54.1.2" {
		t.Fatalf("direct visitor = %q, forged headers were trusted", got)
	}

	ipv6 := httptest.NewRequest("POST", "/", nil)
	ipv6.RemoteAddr = "[2a05:45c2:1:2:3:4:5:6]:40000"
	other := httptest.NewRequest("POST", "/", nil)
	other.RemoteAddr = "[2a05:45c2:1:2:ffff:4:5:6]:40000"
	if shareVisitorKey(ipv6) != shareVisitorKey(other) {
		t.Fatal("addresses in one IPv6 /64 were treated as different visitors")
	}
}

func TestHumanWait(t *testing.T) {
	cases := map[time.Duration]string{
		time.Minute: "1 minute", 30 * time.Minute: "30 minutes", 24 * time.Hour: "24 hours",
		45 * time.Second: "45 seconds", 61 * time.Second: "2 minutes", 59*time.Second + 500*time.Millisecond: "1 minute",
		time.Hour: "1 hour", 90 * time.Minute: "2 hours",
	}
	for wait, want := range cases {
		if got := humanWait(wait); got != want {
			t.Errorf("humanWait(%v) = %q, want %q", wait, got, want)
		}
	}
}

func TestShareUploaderLimit(t *testing.T) {
	clock := &guardClock{now: time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)}
	limit := newShareUploaderLimit(2)
	limit.now = func() time.Time { return clock.now }
	if !limit.allow("1.2.3.4", "a") || !limit.allow("1.2.3.4", "b") {
		t.Fatal("first two accounts refused")
	}
	if !limit.allow("1.2.3.4", "a") {
		t.Fatal("an account already counted was refused")
	}
	if limit.allow("1.2.3.4", "c") {
		t.Fatal("third account from one address allowed")
	}
	if !limit.allow("5.6.7.8", "c") {
		t.Fatal("another address was limited")
	}
	clock.advance(shareUploaderWindow + time.Minute)
	if !limit.allow("1.2.3.4", "c") {
		t.Fatal("limit did not reset after a day")
	}
}

type fakeShareServices struct {
	service.I
	shares service.SharesI
}

func (f fakeShareServices) Shares() service.SharesI { return f.shares }

// fakeShares serves one share from a file. Its unlock cookie is "signed".
type fakeShares struct {
	service.SharesI
	token     string
	record    *model.ShareRecord
	path      string
	downloads int
	checked   int
}

func (f *fakeShares) Public(token string) (*model.ShareRecord, error) {
	if !service.ValidShareToken(token) || token != f.token {
		return nil, service.ErrShareNotFound
	}
	return f.record, nil
}

func (f *fakeShares) Open(token string) (*os.File, *model.ShareRecord, error) {
	record, err := f.Public(token)
	if err != nil {
		return nil, record, err
	}
	file, err := os.Open(f.path)
	return file, record, err
}

func (f *fakeShares) VerifyPassword(token, password string) (*model.ShareRecord, error) {
	f.checked++
	record, err := f.Public(token)
	if err != nil {
		return nil, err
	}
	if record.PasswordHash != "" && password != "right password" {
		return nil, service.ErrSharePassword
	}
	return record, nil
}

func (f *fakeShares) UnlockCookie(string, *model.ShareRecord, time.Time) string { return "signed" }
func (f *fakeShares) ValidUnlockCookie(value, token string, record *model.ShareRecord) bool {
	return value == "signed" && token == f.token && record.PasswordHash != ""
}
func (f *fakeShares) DirectAllowed(*model.ShareRecord) bool { return true }
func (f *fakeShares) CountDownload(string)                  { f.downloads++ }

var testShareToken = strings.Repeat("t", 43)

func newShareTestHandler(t *testing.T, mimeType, body, passwordHash string) (*Handler, *fakeShares) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stored")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	shares := &fakeShares{token: testShareToken, path: path, record: &model.ShareRecord{
		ID: "rec1", Filename: "file.txt", MIMEType: mimeType, Size: int64(len(body)), PasswordHash: passwordHash,
		Downloads: 7, ExpiresAt: time.Now().Add(time.Hour),
	}}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewHandler(logger, fakeShareServices{shares: shares}, &config.Config{AppURL: "https://goport.test"}), shares
}

type shareRequest struct {
	method, target, token, body, remote string
	headers                             map[string]string
}

func serveShare(handle func(*core.RequestEvent) error, request shareRequest) *httptest.ResponseRecorder {
	if request.method == "" {
		request.method = http.MethodGet
	}
	if request.token == "" {
		request.token = testShareToken
	}
	req := httptest.NewRequest(request.method, "/api/v1/public/shares/x"+request.target, strings.NewReader(request.body))
	req.SetPathValue("token", request.token)
	req.RemoteAddr = "84.54.1.2:40000"
	if request.remote != "" {
		req.RemoteAddr = request.remote
	}
	for name, value := range request.headers {
		req.Header.Set(name, value)
	}
	recorder := httptest.NewRecorder()
	// PocketBase's CORS middleware sets this before any handler runs.
	recorder.Header().Set("Access-Control-Allow-Origin", "*")
	event := &core.RequestEvent{}
	event.Request, event.Response = req, recorder
	_ = handle(event)
	return recorder
}

func TestShareDownloadsCountWhatIsSent(t *testing.T) {
	body := strings.Repeat("0123456789", 100)
	h, shares := newShareTestHandler(t, "text/plain; charset=utf-8", body, "")
	cases := []struct {
		method  string
		headers map[string]string
		counted bool
	}{
		{http.MethodGet, nil, true},
		{http.MethodGet, map[string]string{"Range": "bytes=0-"}, true},
		{http.MethodGet, map[string]string{"Range": "bytes=-999999999"}, true},
		{http.MethodGet, map[string]string{"Range": "bytes=00-"}, true},
		{http.MethodGet, map[string]string{"Range": "bytes= 0-"}, true},
		{http.MethodGet, map[string]string{"Range": "bytes=1-", "If-Range": "x"}, true},
		{http.MethodGet, map[string]string{"Range": "bytes=0-0,5-9"}, true},
		{http.MethodGet, map[string]string{"Range": "bytes=100-199"}, false},
		{http.MethodHead, nil, false},
	}
	for _, test := range cases {
		before := shares.downloads
		recorder := serveShare(h.ShareContentHandler, shareRequest{method: test.method, target: "/content", headers: test.headers})
		if counted := shares.downloads > before; counted != test.counted {
			t.Errorf("%s %v: status %d, Content-Range %q, counted %v", test.method, test.headers, recorder.Code, recorder.Header().Get("Content-Range"), counted)
		}
	}
}

func TestSharedPagesPreviewOnlyInAFrame(t *testing.T) {
	h, _ := newShareTestHandler(t, "text/html; charset=utf-8", "<p>hello</p>", "")
	for _, dest := range []string{"document", ""} {
		recorder := serveShare(h.ShareContentHandler, shareRequest{target: "/content?preview=1", headers: map[string]string{"Sec-Fetch-Dest": dest}})
		if !strings.HasPrefix(recorder.Header().Get("Content-Disposition"), "attachment") || recorder.Header().Get("Content-Type") != "application/octet-stream" {
			t.Errorf("top-level page (dest %q) served as %q, %q", dest, recorder.Header().Get("Content-Type"), recorder.Header().Get("Content-Disposition"))
		}
	}
	framed := serveShare(h.ShareContentHandler, shareRequest{target: "/content?preview=1", headers: map[string]string{"Sec-Fetch-Dest": "iframe"}})
	if !strings.HasPrefix(framed.Header().Get("Content-Disposition"), "inline") || !strings.Contains(framed.Header().Get("Content-Security-Policy"), "sandbox allow-scripts") {
		t.Fatalf("framed page served as %q with policy %q", framed.Header().Get("Content-Disposition"), framed.Header().Get("Content-Security-Policy"))
	}
	// Direct links keep showing pages, always sandboxed.
	direct := serveShare(h.ShareRawHandler, shareRequest{target: "/raw", headers: map[string]string{"Sec-Fetch-Dest": "document"}})
	if !strings.HasPrefix(direct.Header().Get("Content-Disposition"), "inline") || !strings.Contains(direct.Header().Get("Content-Security-Policy"), "sandbox allow-scripts") {
		t.Fatalf("direct page served as %q with policy %q", direct.Header().Get("Content-Disposition"), direct.Header().Get("Content-Security-Policy"))
	}
}

func TestShareResponsesAllowOnlyTheSiteOrigin(t *testing.T) {
	h, _ := newShareTestHandler(t, "text/plain; charset=utf-8", "hello", "")
	for _, handle := range []func(*core.RequestEvent) error{h.ShareContentHandler, h.ShareRawHandler, h.PublicShareHandler} {
		other := serveShare(handle, shareRequest{headers: map[string]string{"Origin": "https://evil.test"}})
		if value := other.Header().Get("Access-Control-Allow-Origin"); value != "" {
			t.Errorf("another site was allowed: %q", value)
		}
		site := serveShare(handle, shareRequest{headers: map[string]string{"Origin": "https://goport.test"}})
		if value := site.Header().Get("Access-Control-Allow-Origin"); value != "https://goport.test" {
			t.Errorf("the site was not allowed: %q", value)
		}
	}
}

func TestUnlockKeepsNoStateForUnknownLinks(t *testing.T) {
	h, shares := newShareTestHandler(t, "text/plain", "hello", "$2a$hash")
	for _, token := range []string{strings.Repeat("A", 1<<20), "short", strings.Repeat("B", 43)} {
		recorder := serveShare(h.UnlockShareHandler, shareRequest{method: http.MethodPost, target: "/unlock", token: token, body: `{"password":"guess"}`})
		if recorder.Code != http.StatusNotFound {
			t.Errorf("unknown link answered %d", recorder.Code)
		}
	}
	if len(h.sharePasswords.visitors) != 0 || len(h.sharePasswords.links) != 0 || shares.checked != 0 {
		t.Fatalf("guard kept %d visitors and %d links, %d checks", len(h.sharePasswords.visitors), len(h.sharePasswords.links), shares.checked)
	}
}

func TestUnlockWithoutAPasswordSetsNoCookie(t *testing.T) {
	h, _ := newShareTestHandler(t, "text/plain", "hello", "")
	recorder := serveShare(h.UnlockShareHandler, shareRequest{method: http.MethodPost, target: "/unlock", body: `{"password":""}`})
	if recorder.Code != http.StatusOK || len(recorder.Result().Cookies()) != 0 {
		t.Fatalf("unlock answered %d with cookies %v", recorder.Code, recorder.Result().Cookies())
	}
}

func TestLockedShareRevealsNothingUntilUnlocked(t *testing.T) {
	h, _ := newShareTestHandler(t, "application/pdf", "%PDF-1.7", "$2a$hash")
	locked := serveShare(h.PublicShareHandler, shareRequest{})
	for _, leak := range []string{"file.txt", "application/pdf", "rec1", "downloads"} {
		if strings.Contains(locked.Body.String(), leak) {
			t.Errorf("locked metadata %s reveals %q", locked.Body.String(), leak)
		}
	}
	if !strings.Contains(locked.Body.String(), `"locked":true`) {
		t.Fatalf("locked metadata = %s", locked.Body.String())
	}
	if content := serveShare(h.ShareContentHandler, shareRequest{target: "/content"}); content.Code != http.StatusUnauthorized {
		t.Fatalf("locked content answered %d", content.Code)
	}
	unlock := serveShare(h.UnlockShareHandler, shareRequest{method: http.MethodPost, target: "/unlock", body: `{"password":"right password"}`})
	cookies := unlock.Result().Cookies()
	if unlock.Code != http.StatusOK || len(cookies) != 1 || cookies[0].Value != "signed" {
		t.Fatalf("unlock answered %d with cookies %v", unlock.Code, cookies)
	}
	unlocked := serveShare(h.PublicShareHandler, shareRequest{headers: map[string]string{"Cookie": shareUnlockCookie + "=signed"}})
	if !strings.Contains(unlocked.Body.String(), "file.txt") || strings.Contains(unlocked.Body.String(), `"locked":true`) {
		t.Fatalf("unlocked metadata = %s", unlocked.Body.String())
	}
}

func TestShareSlotsLimitEachVisitor(t *testing.T) {
	slots := newShareSlots(2)
	ctx := context.Background()
	first, ok1 := slots.acquire(ctx, "a", 0)
	second, ok2 := slots.acquire(ctx, "a", 0)
	if !ok1 || !ok2 {
		t.Fatal("slots within the limit refused")
	}
	if _, ok := slots.acquire(ctx, "a", 20*time.Millisecond); ok {
		t.Fatal("slot over the limit granted")
	}
	other, ok := slots.acquire(ctx, "b", 0)
	if !ok {
		t.Fatal("another visitor was limited")
	}
	go func() {
		time.Sleep(20 * time.Millisecond)
		first()
	}()
	third, ok := slots.acquire(ctx, "a", time.Second)
	if !ok {
		t.Fatal("a waiting visitor did not get the freed slot")
	}
	first() // a second release is a no-op
	second()
	third()
	other()
	if len(slots.visitors) != 0 {
		t.Fatalf("slots kept %d visitors after every release", len(slots.visitors))
	}
}

func TestDownloadSlotsSkipTheWebsiteProxy(t *testing.T) {
	previous := shareSlotWait
	shareSlotWait = 10 * time.Millisecond
	defer func() { shareSlotWait = previous }()
	h, _ := newShareTestHandler(t, "text/plain", "hello", "")

	visitor := httptest.NewRequest(http.MethodGet, "/", nil)
	visitor.RemoteAddr = "84.54.1.2:40000"
	var releases []func()
	for range shareVisitorDownloads {
		release, ok := h.takeDownloadSlot(visitor)
		if !ok {
			t.Fatal("download within the visitor limit refused")
		}
		releases = append(releases, release)
	}
	if _, ok := h.takeDownloadSlot(visitor); ok {
		t.Fatal("download over the visitor limit allowed")
	}

	// The website's server fetching direct links names no outside client.
	website := httptest.NewRequest(http.MethodGet, "/", nil)
	website.RemoteAddr = "172.18.0.5:40000"
	website.Header.Set("X-Real-IP", "172.18.0.1")
	for range 2 * shareVisitorDownloads {
		release, ok := h.takeDownloadSlot(website)
		if !ok {
			t.Fatal("website proxy was limited like one visitor")
		}
		releases = append(releases, release)
	}
	for _, release := range releases {
		release()
	}
	if len(h.shareDownloads) != 0 {
		t.Fatalf("%d server slots still held", len(h.shareDownloads))
	}
}

func TestShareVisitorThroughTheWebsite(t *testing.T) {
	proxied := httptest.NewRequest(http.MethodGet, "/", nil)
	proxied.RemoteAddr = "172.18.0.5:40000"
	proxied.Header.Set("X-Real-IP", "172.18.0.1")
	proxied.Header.Set("X-Forwarded-For", "84.54.1.9, 172.18.0.1")
	if key, internal := shareVisitor(proxied); key != "84.54.1.9" || internal {
		t.Fatalf("visitor behind the website = %q, internal %v", key, internal)
	}
	proxied.Header.Del("X-Forwarded-For")
	if key, internal := shareVisitor(proxied); key != "172.18.0.1" || !internal {
		t.Fatalf("website without a client = %q, internal %v", key, internal)
	}
	direct := httptest.NewRequest(http.MethodGet, "/", nil)
	direct.RemoteAddr = "84.54.1.2:40000"
	direct.Header.Set("X-Real-IP", "10.0.0.1")
	if key, internal := shareVisitor(direct); key != "84.54.1.2" || internal {
		t.Fatalf("forged internal address = %q, internal %v", key, internal)
	}
}

func TestShareWriteDeadline(t *testing.T) {
	started := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	if got := shareWriteDeadline(started, started, shareMinRate); !got.Equal(started.Add(shareWriteGrace + time.Second)) {
		t.Fatalf("first write due %v", got.Sub(started))
	}
	// Far ahead of schedule, a stalled write still ends within the idle limit.
	now := started.Add(time.Minute)
	if got := shareWriteDeadline(started, now, 1<<30); !got.Equal(now.Add(shareWriteIdle)) {
		t.Fatalf("idle limit = %v", got.Sub(now))
	}
	// A reader far behind schedule is already late.
	if got := shareWriteDeadline(started, started.Add(time.Hour), shareMinRate); !got.Before(started.Add(time.Hour)) {
		t.Fatal("slow reader got more time")
	}
}

func TestStalledDownloadIsCutOff(t *testing.T) {
	previousGrace, previousIdle := shareWriteGrace, shareWriteIdle
	shareWriteGrace, shareWriteIdle = 200*time.Millisecond, 200*time.Millisecond
	defer func() { shareWriteGrace, shareWriteIdle = previousGrace, previousIdle }()

	result := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// PocketBase hands handlers its own wrapper; the deadline must reach
		// the connection through it.
		writer := newShareDownloadWriter(&router.ResponseWriter{ResponseWriter: w}, r.Method, nil)
		chunk := make([]byte, 64<<10)
		for sent := 0; sent < 256<<20; sent += len(chunk) {
			if _, err := writer.Write(chunk); err != nil {
				result <- err
				return
			}
		}
		result <- nil
	}))
	defer server.Close()

	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("GET / HTTP/1.1\r\nHost: test\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	// The client never reads.
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("256 MB went to a client that never reads")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("stalled download was not cut off")
	}
}
