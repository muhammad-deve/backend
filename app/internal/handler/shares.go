package handler

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/model"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/service"
	"gitlab.yurtal.tech/company/pocketbase-app-template/pkg/disposable"
)

const publicSharesPath = "/api/v1/public/shares/"

func (h *Handler) ListSharesHandler(e *core.RequestEvent) error {
	items, usage, limits, err := h.service.Shares().List(e.Auth.Id)
	if err != nil {
		h.logger.Error("failed to list shares", "error", err, "userId", e.Auth.Id)
		return h.NewErrorResponse(e, http.StatusInternalServerError, "Couldn't load your shared files.")
	}
	return h.NewSuccessResponse(e, http.StatusOK, map[string]any{
		"items": items, "storage": usage, "limits": limits,
		"maxUploadBytes": limits.Current.MaxUploadBytes, "chunkSize": h.service.Shares().ChunkSize(),
	})
}

// UploadShareHandler accepts a whole file in one multipart request. Requests
// above 32 MB never reach it (PocketBase's body limit), so the dashboard
// uses the chunked endpoints below.
func (h *Handler) UploadShareHandler(e *core.RequestEvent) error {
	select {
	case h.shareUploads <- struct{}{}:
		defer func() { <-h.shareUploads }()
	default:
		return h.NewErrorResponse(e, http.StatusTooManyRequests, "Too many uploads are in progress. Try again shortly.")
	}
	limits, allowed, err := h.shareUploadAllowed(e)
	if !allowed {
		return err
	}
	e.Request.Body = http.MaxBytesReader(e.Response, e.Request.Body, limits.Current.MaxUploadBytes+1024*1024)
	if err := e.Request.ParseMultipartForm(64 << 10); err != nil {
		return h.NewErrorResponse(e, http.StatusRequestEntityTooLarge, "The upload is too large or invalid.")
	}
	if e.Request.MultipartForm != nil {
		defer e.Request.MultipartForm.RemoveAll()
	}
	file, header, err := e.Request.FormFile("file")
	if err != nil {
		return h.NewErrorResponse(e, http.StatusBadRequest, "Choose a file to upload.")
	}
	defer file.Close()
	expiresAt, err := parseShareExpiration(e.Request.FormValue("expiresAt"))
	if err != nil {
		return h.NewErrorResponse(e, http.StatusBadRequest, err.Error())
	}
	direct := e.Request.FormValue("direct") == "true"
	item, err := h.service.Shares().Create(e.Auth.Id, header.Filename, file, expiresAt, e.Request.FormValue("password"), direct)
	if err != nil {
		return h.shareError(e, err)
	}
	return h.NewSuccessResponse(e, http.StatusCreated, item)
}

func (h *Handler) StartShareUploadHandler(e *core.RequestEvent) error {
	request := &model.StartShareUploadRequest{}
	if err := e.BindBody(request); err != nil {
		return h.NewErrorResponse(e, http.StatusBadRequest, "invalid request body")
	}
	expiresAt, err := parseShareExpiration(request.ExpiresAt)
	if err != nil {
		return h.NewErrorResponse(e, http.StatusBadRequest, err.Error())
	}
	if _, allowed, err := h.shareUploadAllowed(e); !allowed {
		return err
	}
	session, err := h.service.Shares().StartUpload(e.Auth.Id, *request, expiresAt)
	if err != nil {
		return h.shareError(e, err)
	}
	return h.NewSuccessResponse(e, http.StatusCreated, session)
}

// shareUploadAllowed applies the account checks every cloud upload needs and
// returns the uploader's limits. When allowed is false the refusal has been
// written already, and the caller returns err as it is.
func (h *Handler) shareUploadAllowed(e *core.RequestEvent) (limits model.ShareLimits, allowed bool, err error) {
	if !e.Auth.Verified() {
		return limits, false, h.NewErrorResponse(e, http.StatusForbidden, "Confirm your email address before uploading files.")
	}
	if disposable.Email(e.Auth.Email()) {
		return limits, false, h.NewErrorResponse(e, http.StatusForbidden, "Cloud uploads need a regular email address. Change your email in Profile to upload files.")
	}
	limits, err = h.service.Shares().Limits(e.Auth.Id)
	if err != nil {
		return limits, false, h.shareError(e, err)
	}
	if !limits.IsPro && !h.shareUploaders.allow(shareVisitorKey(e.Request), e.Auth.Id) {
		return limits, false, h.NewErrorResponse(e, http.StatusTooManyRequests, "Too many Free accounts uploaded from your network today. Try again tomorrow or upgrade to Pro.")
	}
	return limits, true, nil
}

func (h *Handler) ShareUploadChunkHandler(e *core.RequestEvent) error {
	offset, err := strconv.ParseInt(e.Request.URL.Query().Get("offset"), 10, 64)
	if err != nil {
		return h.NewErrorResponse(e, http.StatusBadRequest, "offset is required")
	}
	e.Request.Body = http.MaxBytesReader(e.Response, e.Request.Body, h.service.Shares().ChunkSize()+1)
	received, err := h.service.Shares().WriteUploadChunk(e.Auth.Id, e.Request.PathValue("id"), offset, e.Request.Body)
	var offsetErr *service.UploadOffsetError
	switch {
	case errors.As(err, &offsetErr):
		return e.JSON(http.StatusConflict, map[string]any{"error": "The upload continues from a different point.", "received": offsetErr.Received})
	case err != nil:
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return h.NewErrorResponse(e, http.StatusRequestEntityTooLarge, "This chunk is larger than allowed.")
		}
		return h.shareError(e, err)
	}
	return h.NewSuccessResponse(e, http.StatusOK, map[string]int64{"received": received})
}

func (h *Handler) CompleteShareUploadHandler(e *core.RequestEvent) error {
	item, err := h.service.Shares().CompleteUpload(e.Auth.Id, e.Request.PathValue("id"))
	if err != nil {
		return h.shareError(e, err)
	}
	return h.NewSuccessResponse(e, http.StatusCreated, item)
}

func (h *Handler) CancelShareUploadHandler(e *core.RequestEvent) error {
	if err := h.service.Shares().CancelUpload(e.Auth.Id, e.Request.PathValue("id")); err != nil {
		return h.shareError(e, err)
	}
	return h.NewSuccessResponse(e, http.StatusOK, map[string]string{"message": "Upload cancelled"})
}

func (h *Handler) UpdateShareHandler(e *core.RequestEvent) error {
	request := &model.UpdateShareRequest{}
	if err := e.BindBody(request); err != nil {
		return h.NewErrorResponse(e, http.StatusBadRequest, "invalid request body")
	}
	expiresAt, err := parseShareExpiration(request.ExpiresAt)
	if err != nil {
		return h.NewErrorResponse(e, http.StatusBadRequest, err.Error())
	}
	item, err := h.service.Shares().Update(e.Auth.Id, e.Request.PathValue("id"), expiresAt, request.Password, request.Direct)
	if err != nil {
		return h.shareError(e, err)
	}
	return h.NewSuccessResponse(e, http.StatusOK, item)
}

func (h *Handler) RevokeShareHandler(e *core.RequestEvent) error {
	if err := h.service.Shares().Revoke(e.Auth.Id, e.Request.PathValue("id")); err != nil {
		return h.shareError(e, err)
	}
	return h.NewSuccessResponse(e, http.StatusOK, map[string]string{"message": "Share revoked"})
}

func (h *Handler) RestoreShareHandler(e *core.RequestEvent) error {
	if err := h.service.Shares().Restore(e.Auth.Id, e.Request.PathValue("id")); err != nil {
		return h.shareError(e, err)
	}
	return h.NewSuccessResponse(e, http.StatusOK, map[string]string{"message": "Share restored"})
}

func (h *Handler) DeleteShareHandler(e *core.RequestEvent) error {
	if err := h.service.Shares().Delete(e.Auth.Id, e.Request.PathValue("id")); err != nil {
		return h.shareError(e, err)
	}
	return h.NewSuccessResponse(e, http.StatusOK, map[string]string{"message": "File deleted"})
}

func (h *Handler) PublicShareHandler(e *core.RequestEvent) error {
	h.publicShareHeaders(e.Response, e.Request)
	token := e.Request.PathValue("token")
	item, _, err := h.service.Shares().Public(token)
	if err != nil {
		return h.publicShareError(e, err)
	}
	if item.PasswordProtected {
		item.RetryAfter = waitSeconds(h.sharePasswords.status(token, shareVisitorKey(e.Request)))
	}
	return h.NewSuccessResponse(e, http.StatusOK, item)
}

func (h *Handler) PublicShareOptionsHandler(e *core.RequestEvent) error {
	h.publicShareHeaders(e.Response, e.Request)
	e.Response.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, POST, OPTIONS")
	e.Response.Header().Set("Access-Control-Allow-Headers", "Content-Type, Range")
	e.Response.Header().Set("Access-Control-Max-Age", "600")
	e.Response.WriteHeader(http.StatusNoContent)
	return nil
}

// publicSharePreflight answers CORS preflights for the public share API
// before PocketBase's own CORS middleware does. That middleware replies with
// "Access-Control-Allow-Origin: *" and no credentials flag, which browsers
// reject for the credentialed unlock request, so it failed with "Failed to
// fetch" before reaching the server.
func (h *Handler) publicSharePreflight(e *core.RequestEvent) error {
	if e.Request.Method == http.MethodOptions && strings.HasPrefix(e.Request.URL.Path, publicSharesPath) && !h.service.TCP().IsTunnelHost(e.Request.Host) {
		return h.PublicShareOptionsHandler(e)
	}
	return e.Next()
}

func (h *Handler) UnlockShareHandler(e *core.RequestEvent) error {
	h.publicShareHeaders(e.Response, e.Request)
	e.Request.Body = http.MaxBytesReader(e.Response, e.Request.Body, 4096)
	var request struct {
		Password string `json:"password"`
	}
	if json.NewDecoder(e.Request.Body).Decode(&request) != nil {
		return h.NewErrorResponse(e, http.StatusBadRequest, "invalid request body")
	}
	token := e.Request.PathValue("token")
	visitor := shareVisitorKey(e.Request)
	if wait, reason := h.sharePasswords.begin(token, visitor); wait > 0 {
		return h.sharePasswordPaused(e, wait, reason)
	}
	outcome := shareAttemptOther
	finished := false
	finish := func() (time.Duration, shareLockReason, int) {
		finished = true
		return h.sharePasswords.finish(token, visitor, outcome)
	}
	// Always release the reservation, even if verification panics.
	defer func() {
		if !finished {
			h.sharePasswords.finish(token, visitor, shareAttemptOther)
		}
	}()

	record, err := h.service.Shares().VerifyPassword(token, request.Password)
	switch {
	case errors.Is(err, service.ErrSharePassword):
		outcome = shareAttemptWrong
		wait, reason, left := finish()
		time.Sleep(150 * time.Millisecond)
		if wait > 0 {
			return h.sharePasswordPaused(e, wait, reason)
		}
		return e.JSON(http.StatusUnauthorized, map[string]any{"state": "password", "error": "Wrong password.", "attemptsLeft": left})
	case err != nil:
		finish()
		return h.publicShareError(e, err)
	}
	outcome = shareAttemptCorrect
	finish()
	expires := time.Now().Add(12 * time.Hour)
	if record.ExpiresAt.Before(expires) {
		expires = record.ExpiresAt
	}
	http.SetCookie(e.Response, &http.Cookie{Name: "goport_cloud_share", Value: h.shareCookie(token, expires), Path: publicSharesPath + token, Expires: expires, HttpOnly: true, Secure: requestIsHTTPS(e.Request), SameSite: http.SameSiteLaxMode})
	return h.NewSuccessResponse(e, http.StatusOK, map[string]bool{"unlocked": true})
}

func (h *Handler) sharePasswordPaused(e *core.RequestEvent, wait time.Duration, reason shareLockReason) error {
	seconds := waitSeconds(wait)
	e.Response.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
	message := "Too many wrong passwords. Try again in " + humanWait(wait) + "."
	switch reason {
	case shareLockLink:
		message = "This link received too many wrong passwords and is paused. Try again in " + humanWait(wait) + "."
	case shareLockBusy:
		message = "Your last attempt is still being checked."
	}
	return e.JSON(http.StatusTooManyRequests, map[string]any{"state": "locked", "reason": reason, "error": message, "retryAfter": seconds})
}

func (h *Handler) ShareContentHandler(e *core.RequestEvent) error {
	h.publicShareHeaders(e.Response, e.Request)
	select {
	case h.shareDownloads <- struct{}{}:
		defer func() { <-h.shareDownloads }()
	default:
		return h.NewErrorResponse(e, http.StatusTooManyRequests, "Too many downloads are in progress. Try again shortly.")
	}
	token := e.Request.PathValue("token")
	item, record, err := h.service.Shares().Public(token)
	if err != nil {
		return h.publicShareError(e, err)
	}
	if record.PasswordHash != "" && !h.validShareCookie(e.Request, token) {
		return h.NewErrorResponse(e, http.StatusUnauthorized, "password required")
	}
	preview := e.Request.URL.Query().Get("preview") == "1" && item.Previewable
	return h.serveShareFile(e, token, preview, countsAsDownload(e.Request))
}

// countsAsDownload reports whether a request delivers the file from the
// start. Previews count as well as downloads, since both cost the same
// traffic; follow-up range requests from a PDF viewer do not.
func countsAsDownload(r *http.Request) bool {
	rangeHeader := r.Header.Get("Range")
	return r.Method == http.MethodGet && (rangeHeader == "" || strings.HasPrefix(rangeHeader, "bytes=0-"))
}

// ShareRawHandler serves a direct link: the file itself, with no share page.
// goport.uz/f/<token>/<name> proxies here. Anything that is not an active
// direct link answers 409 with state "page", and the site sends the visitor to
// the share page instead.
func (h *Handler) ShareRawHandler(e *core.RequestEvent) error {
	h.publicShareHeaders(e.Response, e.Request)
	select {
	case h.shareDownloads <- struct{}{}:
		defer func() { <-h.shareDownloads }()
	default:
		return h.NewErrorResponse(e, http.StatusTooManyRequests, "Too many downloads are in progress. Try again shortly.")
	}
	token := e.Request.PathValue("token")
	item, record, err := h.service.Shares().Public(token)
	if err != nil {
		return h.publicShareError(e, err)
	}
	if !h.service.Shares().DirectAllowed(record) {
		return e.JSON(http.StatusConflict, map[string]string{"state": "page", "error": "This file opens on its share page."})
	}
	return h.serveShareFile(e, token, item.Previewable, countsAsDownload(e.Request))
}

func (h *Handler) serveShareFile(e *core.RequestEvent, token string, inline, countDownload bool) error {
	file, record, err := h.service.Shares().Open(token)
	if err != nil {
		return h.publicShareError(e, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return h.NewErrorResponse(e, http.StatusInternalServerError, "Couldn't open this file.")
	}
	contentType, disposition := "application/octet-stream", "attachment"
	if inline {
		contentType, disposition = record.MIMEType, "inline"
	}
	e.Response.Header().Set("Content-Type", contentType)
	e.Response.Header().Set("Content-Disposition", contentDisposition(disposition, record.Filename))
	// Revoking or deleting must take effect at once, so nothing is cached.
	e.Response.Header().Set("Cache-Control", "private, no-store")
	if countDownload {
		h.service.Shares().CountDownload(record.ID)
	}
	http.ServeContent(e.Response, e.Request, record.Filename, info.ModTime(), file)
	return nil
}

func (h *Handler) shareError(e *core.RequestEvent, err error) error {
	switch {
	case errors.Is(err, service.ErrShareNotFound):
		return h.NewErrorResponse(e, http.StatusNotFound, "Shared file not found.")
	case errors.Is(err, service.ErrShareTooLarge):
		return h.NewErrorResponse(e, http.StatusRequestEntityTooLarge, "The file exceeds your plan's upload limit.")
	case errors.Is(err, service.ErrShareQuota):
		return h.NewErrorResponse(e, http.StatusInsufficientStorage, "Your storage is full. Delete files or upgrade to Pro for more space.")
	case errors.Is(err, service.ErrShareType):
		return h.NewErrorResponse(e, http.StatusUnsupportedMediaType, "This file type is not allowed.")
	case errors.Is(err, service.ErrShareEmpty):
		return h.NewErrorResponse(e, http.StatusBadRequest, "This file is empty.")
	case errors.Is(err, service.ErrShareBusy):
		return h.NewErrorResponse(e, http.StatusTooManyRequests, "Too many uploads are in progress. Try again shortly.")
	case errors.Is(err, service.ErrShareDirectPro):
		return h.NewErrorResponse(e, http.StatusForbidden, "Direct links are part of GoPort Pro.")
	case errors.Is(err, service.ErrShareDirectPassword):
		return h.NewErrorResponse(e, http.StatusBadRequest, "Direct links open the file itself, so they can't ask for a password.")
	case errors.Is(err, service.ErrShareUploadNotFound):
		return h.NewErrorResponse(e, http.StatusNotFound, "This upload was interrupted. Start it again.")
	case errors.Is(err, service.ErrShareUploadIncomplete):
		return h.NewErrorResponse(e, http.StatusConflict, "The upload hasn't finished yet.")
	case errors.Is(err, service.ErrShareExpired):
		return h.NewErrorResponse(e, http.StatusConflict, "This link has already expired.")
	case errors.Is(err, service.ErrShareFull):
		return h.NewErrorResponse(e, http.StatusInsufficientStorage, "GoPort cloud storage is full right now. Try again later.")
	}
	var expiry *service.ErrShareExpiry
	switch {
	case errors.As(err, &expiry):
		return h.NewErrorResponse(e, http.StatusBadRequest, expiry.Message)
	default:
		h.logger.Error("share request failed", "error", err)
		return h.NewErrorResponse(e, http.StatusBadRequest, err.Error())
	}
}

func (h *Handler) publicShareError(e *core.RequestEvent, err error) error {
	switch {
	case errors.Is(err, service.ErrShareExpired):
		return e.JSON(http.StatusGone, map[string]string{"state": "expired", "error": "This link has expired."})
	case errors.Is(err, service.ErrShareRevoked):
		return e.JSON(http.StatusGone, map[string]string{"state": "revoked", "error": "This link has been revoked."})
	case errors.Is(err, service.ErrShareDownloadLimit):
		return e.JSON(http.StatusGone, map[string]string{"state": "limit", "error": "This file reached its download limit."})
	case errors.Is(err, service.ErrSharePassword):
		return e.JSON(http.StatusUnauthorized, map[string]string{"state": "password", "error": "Wrong password."})
	default:
		return e.JSON(http.StatusNotFound, map[string]string{"state": "invalid", "error": "This sharing link is invalid or deleted."})
	}
}

func (h *Handler) publicShareHeaders(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	frameOrigin := "'self'"
	if appURL, err := url.Parse(h.cfg.AppURL); err == nil && appURL.Scheme != "" && appURL.Host != "" {
		frameOrigin += " " + appURL.Scheme + "://" + appURL.Host
	}
	w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors "+frameOrigin+"; object-src 'none'")
	w.Header().Add("Vary", "Origin")
	if origin := r.Header.Get("Origin"); origin != "" && strings.TrimRight(origin, "/") == strings.TrimRight(h.cfg.AppURL, "/") {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Access-Control-Expose-Headers", "Retry-After, Content-Length, Content-Range")
	}
}

func parseShareExpiration(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("expiration must use RFC3339 format")
	}
	return parsed, nil
}

func (h *Handler) shareCookie(token string, expires time.Time) string {
	payload := strconv.FormatInt(expires.Unix(), 10) + "." + token
	mac := hmac.New(sha256.New, []byte(h.shareSecret()))
	_, _ = mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + hex.EncodeToString(mac.Sum(nil))
}

func (h *Handler) validShareCookie(r *http.Request, token string) bool {
	cookie, err := r.Cookie("goport_cloud_share")
	if err != nil {
		return false
	}
	parts := strings.Split(cookie.Value, ".")
	if len(parts) != 2 {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false
	}
	payload := string(raw)
	segments := strings.SplitN(payload, ".", 2)
	if len(segments) != 2 || segments[1] != token {
		return false
	}
	expiry, err := strconv.ParseInt(segments[0], 10, 64)
	if err != nil || time.Now().Unix() >= expiry {
		return false
	}
	mac := hmac.New(sha256.New, []byte(h.shareSecret()))
	_, _ = mac.Write(raw)
	want := hex.EncodeToString(mac.Sum(nil))
	return subtle.ConstantTimeCompare([]byte(want), []byte(parts[1])) == 1
}

func (h *Handler) shareSecret() string {
	if h.cfg.ShareSigningKey != "" {
		return h.cfg.ShareSigningKey
	}
	return "goport-development-share-key-change-in-production"
}

func contentDisposition(kind, filename string) string {
	safe := strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == '"' {
			return -1
		}
		return r
	}, filepath.Base(filename))
	// filename= must be plain ASCII; browsers read the real name from
	// filename*.
	fallback := strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e || r == '\\' {
			return '_'
		}
		return r
	}, safe)
	return fmt.Sprintf(`%s; filename="%s"; filename*=UTF-8''%s`, kind, fallback, url.PathEscape(safe))
}

func waitSeconds(wait time.Duration) int64 {
	if wait <= 0 {
		return 0
	}
	return int64(math.Ceil(wait.Seconds()))
}

// humanWait renders a pause as "1 minute", "30 minutes" or "24 hours".
func humanWait(wait time.Duration) string {
	plural := func(value int64, unit string) string {
		if value == 1 {
			return "1 " + unit
		}
		return strconv.FormatInt(value, 10) + " " + unit + "s"
	}
	seconds := max(waitSeconds(wait), 1)
	switch {
	case seconds >= 3600:
		return plural((seconds+3599)/3600, "hour")
	case seconds >= 60:
		return plural((seconds+59)/60, "minute")
	default:
		return plural(seconds, "second")
	}
}

func requestIsHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}
