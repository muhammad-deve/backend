package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/model"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/service"
)

func testEvent() (*core.RequestEvent, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	e := &core.RequestEvent{}
	e.Response = rec
	e.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	return e, rec
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	body := map[string]any{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return body
}

func TestErrorResponseHidesInternalErrors(t *testing.T) {
	h := &Handler{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	e, rec := testEvent()
	_ = h.errorResponse(e, fmt.Errorf("failed to create user: UNIQUE constraint failed: users.email"), "Couldn't do that.", "failed")
	if rec.Code != http.StatusInternalServerError || decodeBody(t, rec)["error"] != "Couldn't do that." {
		t.Fatalf("internal error leaked: %d %s", rec.Code, rec.Body.String())
	}

	e, rec = testEvent()
	_ = h.errorResponse(e, fmt.Errorf("wrapped: %w", service.NewUserError(model.ErrNameTooLong.Error())), "Couldn't do that.", "failed")
	if rec.Code != http.StatusBadRequest || decodeBody(t, rec)["error"] != model.ErrNameTooLong.Error() {
		t.Fatalf("user message lost: %d %s", rec.Code, rec.Body.String())
	}
}

func TestTooManyAttempts(t *testing.T) {
	e, rec := testEvent()
	_ = TooManyAttempts(e, 125)
	body := decodeBody(t, rec)
	if rec.Code != http.StatusTooManyRequests || body["error"] != "Too many attempts. Try again in 3 minutes." || body["retryAfter"] != float64(125) {
		t.Fatalf("429 body = %s", rec.Body.String())
	}
	if rec.Header().Get("Retry-After") != "125" {
		t.Fatalf("Retry-After = %q", rec.Header().Get("Retry-After"))
	}

	e, rec = testEvent()
	_ = TooManyAttempts(e, 30)
	if decodeBody(t, rec)["error"] != "Too many attempts. Try again in 1 minute." {
		t.Fatalf("429 body = %s", rec.Body.String())
	}
}

func TestClientIPTrustsProxyHeadersOnlyFromPrivateAddresses(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	request.RemoteAddr = "172.18.0.5:41000"
	request.Header.Set("X-Forwarded-For", "6.6.6.6, 203.0.113.9")
	if got := ClientIP(request); got != "203.0.113.9" {
		t.Fatalf("behind nginx = %q", got)
	}
	request.Header.Set("X-Real-IP", "198.51.100.7")
	if got := ClientIP(request); got != "198.51.100.7" {
		t.Fatalf("X-Real-IP = %q", got)
	}

	direct := httptest.NewRequest(http.MethodPost, "/", nil)
	direct.RemoteAddr = "203.0.113.50:5000"
	direct.Header.Set("X-Real-IP", "1.2.3.4")
	if got := ClientIP(direct); got != "203.0.113.50" {
		t.Fatalf("direct request must ignore headers, got %q", got)
	}

	v6 := httptest.NewRequest(http.MethodPost, "/", nil)
	v6.RemoteAddr = "[2001:db8:1:2:3:4:5:6]:5000"
	if got := ClientIP(v6); got != "2001:db8:1:2::/64" {
		t.Fatalf("IPv6 = %q", got)
	}
}
