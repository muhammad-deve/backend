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
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/model"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/service"
)

func (h *Handler) ListSharesHandler(e *core.RequestEvent) error {
	items, usage, err := h.service.Shares().List(e.Auth.Id)
	if err != nil {
		h.logger.Error("failed to list shares", "error", err, "userId", e.Auth.Id)
		return h.NewErrorResponse(e, http.StatusInternalServerError, "Couldn't load your shared files.")
	}
	return h.NewSuccessResponse(e, http.StatusOK, map[string]any{"items": items, "storage": usage, "maxUploadBytes": h.service.Shares().MaxUploadBytes()})
}

func (h *Handler) UploadShareHandler(e *core.RequestEvent) error {
	select {
	case h.shareUploads <- struct{}{}:
		defer func() { <-h.shareUploads }()
	default:
		return h.NewErrorResponse(e, http.StatusTooManyRequests, "Too many uploads are in progress. Try again shortly.")
	}
	limit := h.service.Shares().MaxUploadBytes()
	e.Request.Body = http.MaxBytesReader(e.Response, e.Request.Body, limit+1024*1024)
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
	item, err := h.service.Shares().Create(e.Auth.Id, header.Filename, file, expiresAt, e.Request.FormValue("password"))
	if err != nil {
		return h.shareError(e, err)
	}
	return h.NewSuccessResponse(e, http.StatusCreated, item)
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
	item, err := h.service.Shares().Update(e.Auth.Id, e.Request.PathValue("id"), expiresAt, request.Password)
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
func (h *Handler) DeleteShareHandler(e *core.RequestEvent) error {
	if err := h.service.Shares().Delete(e.Auth.Id, e.Request.PathValue("id")); err != nil {
		return h.shareError(e, err)
	}
	return h.NewSuccessResponse(e, http.StatusOK, map[string]string{"message": "File deleted"})
}

func (h *Handler) PublicShareHandler(e *core.RequestEvent) error {
	h.publicShareHeaders(e.Response, e.Request)
	item, _, err := h.service.Shares().Public(e.Request.PathValue("token"))
	if err != nil {
		return h.publicShareError(e, err)
	}
	return h.NewSuccessResponse(e, http.StatusOK, item)
}

func (h *Handler) PublicShareOptionsHandler(e *core.RequestEvent) error {
	h.publicShareHeaders(e.Response, e.Request)
	e.Response.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, POST, OPTIONS")
	e.Response.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	e.Response.WriteHeader(http.StatusNoContent)
	return nil
}

func (h *Handler) UnlockShareHandler(e *core.RequestEvent) error {
	h.publicShareHeaders(e.Response, e.Request)
	if !h.shareAttempts.allow(e.Request.PathValue("token") + "|" + clientIP(e.Request)) {
		e.Response.Header().Set("Retry-After", "900")
		return h.NewErrorResponse(e, http.StatusTooManyRequests, "Too many password attempts. Try again later.")
	}
	e.Request.Body = http.MaxBytesReader(e.Response, e.Request.Body, 4096)
	var request struct {
		Password string `json:"password"`
	}
	if json.NewDecoder(e.Request.Body).Decode(&request) != nil {
		return h.NewErrorResponse(e, http.StatusBadRequest, "invalid request body")
	}
	token := e.Request.PathValue("token")
	record, err := h.service.Shares().VerifyPassword(token, request.Password)
	if err != nil {
		time.Sleep(150 * time.Millisecond)
		return h.publicShareError(e, err)
	}
	expires := time.Now().Add(12 * time.Hour)
	if record.ExpiresAt.Before(expires) {
		expires = record.ExpiresAt
	}
	http.SetCookie(e.Response, &http.Cookie{Name: "goport_cloud_share", Value: h.shareCookie(token, expires), Path: "/api/v1/public/shares/" + token, Expires: expires, HttpOnly: true, Secure: requestIsHTTPS(e.Request), SameSite: http.SameSiteLaxMode})
	return h.NewSuccessResponse(e, http.StatusOK, map[string]bool{"unlocked": true})
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
	file, record, err := h.service.Shares().Open(token)
	if err != nil {
		return h.publicShareError(e, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return h.NewErrorResponse(e, http.StatusInternalServerError, "Couldn't open this file.")
	}
	preview := e.Request.URL.Query().Get("preview") == "1" && item.Previewable
	contentType := record.MIMEType
	if !preview {
		contentType = "application/octet-stream"
	}
	e.Response.Header().Set("Content-Type", contentType)
	disposition := "attachment"
	if preview {
		disposition = "inline"
	}
	e.Response.Header().Set("Content-Disposition", contentDisposition(disposition, record.Filename))
	if e.Request.URL.Query().Get("download") == "1" {
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
		return h.NewErrorResponse(e, http.StatusRequestEntityTooLarge, "The file exceeds the upload limit.")
	case errors.Is(err, service.ErrShareQuota):
		return h.NewErrorResponse(e, http.StatusInsufficientStorage, "Your storage quota is full.")
	case errors.Is(err, service.ErrShareType):
		return h.NewErrorResponse(e, http.StatusUnsupportedMediaType, "This file type is not allowed.")
	default:
		return h.NewErrorResponse(e, http.StatusBadRequest, err.Error())
	}
}
func (h *Handler) publicShareError(e *core.RequestEvent, err error) error {
	switch {
	case errors.Is(err, service.ErrShareExpired):
		return e.JSON(http.StatusGone, map[string]string{"state": "expired", "error": "This link has expired."})
	case errors.Is(err, service.ErrShareRevoked):
		return e.JSON(http.StatusGone, map[string]string{"state": "revoked", "error": "This link has been revoked."})
	case errors.Is(err, service.ErrSharePassword):
		return e.JSON(http.StatusUnauthorized, map[string]string{"state": "password", "error": "Invalid password."})
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
	if origin := r.Header.Get("Origin"); origin != "" && strings.TrimRight(origin, "/") == strings.TrimRight(h.cfg.AppURL, "/") {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Add("Vary", "Origin")
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
	return fmt.Sprintf(`%s; filename="%s"; filename*=UTF-8''%s`, kind, safe, url.PathEscape(safe))
}

func (l *shareAttemptLimiter) allow(key string) bool {
	now := time.Now()
	cutoff := now.Add(-15 * time.Minute)
	l.mu.Lock()
	defer l.mu.Unlock()
	values := l.hits[key]
	if len(l.hits) > 10000 {
		for existingKey, existingValues := range l.hits {
			if len(existingValues) == 0 || existingValues[len(existingValues)-1].Before(cutoff) {
				delete(l.hits, existingKey)
			}
		}
	}
	kept := values[:0]
	for _, value := range values {
		if value.After(cutoff) {
			kept = append(kept, value)
		}
	}
	if len(kept) >= 10 {
		l.hits[key] = kept
		return false
	}
	l.hits[key] = append(kept, now)
	return true
}
func clientIP(r *http.Request) string {
	if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0]); forwarded != "" {
		return forwarded
	}
	value := r.RemoteAddr
	if host, _, err := net.SplitHostPort(value); err == nil {
		return host
	}
	return value
}

func requestIsHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}
