package service

import (
	"bufio"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/config"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/model"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/repository"
	"golang.org/x/crypto/bcrypt"
)

var (
	ErrShareNotFound         = errors.New("share not found")
	ErrShareExpired          = errors.New("share expired")
	ErrShareRevoked          = errors.New("share revoked")
	ErrSharePassword         = errors.New("invalid share password")
	ErrShareQuota            = errors.New("storage quota exceeded")
	ErrShareTooLarge         = errors.New("file is too large")
	ErrShareType             = errors.New("file type is not allowed")
	ErrShareEmpty            = errors.New("file is empty")
	ErrShareBusy             = errors.New("too many uploads in progress")
	ErrShareDirectPro        = errors.New("direct links are a Pro feature")
	ErrShareDirectPassword   = errors.New("direct links can't have a password")
	ErrShareUploadNotFound   = errors.New("upload not found")
	ErrShareUploadIncomplete = errors.New("upload is incomplete")
	ErrShareFull             = errors.New("cloud storage is full")
	ErrShareDownloadLimit    = errors.New("download limit reached")
)

// UploadOffsetError means a chunk did not start where the stored data ends.
// Received tells the client where to resume.
type UploadOffsetError struct{ Received int64 }

func (e *UploadOffsetError) Error() string {
	return fmt.Sprintf("upload continues at byte %d", e.Received)
}

const (
	// Chunks stay well under PocketBase's 32 MB request limit and nginx's
	// 50 MB limit, so file size is bounded only by the plan.
	shareChunkSize         int64 = 8 << 20
	shareMaxUserUploads          = 3
	shareMaxUploads              = 40
	shareUploadIdleTimeout       = 30 * time.Minute
)

type SharesI interface {
	Create(userID, filename string, source io.Reader, expiresAt time.Time, password string, direct bool) (*model.ShareItem, error)
	StartUpload(userID string, request model.StartShareUploadRequest, expiresAt time.Time) (*model.ShareUploadSession, error)
	WriteUploadChunk(userID, uploadID string, offset int64, body io.Reader) (int64, error)
	CompleteUpload(userID, uploadID string) (*model.ShareItem, error)
	CancelUpload(userID, uploadID string) error
	List(userID string) ([]model.ShareItem, model.ShareStorageUsage, model.ShareLimits, error)
	Update(userID, id string, expiresAt time.Time, password *string, direct *bool) (*model.ShareItem, error)
	Revoke(userID, id string) error
	Restore(userID, id string) error
	Delete(userID, id string) error
	Public(token string) (*model.ShareItem, *model.ShareRecord, error)
	VerifyPassword(token, password string) (*model.ShareRecord, error)
	Open(token string) (*os.File, *model.ShareRecord, error)
	DirectAllowed(record *model.ShareRecord) bool
	CountDownload(id string)
	Limits(userID string) (model.ShareLimits, error)
	ChunkSize() int64
	CleanupExpired()
}

type sharesService struct {
	cfg        *config.Config
	repo       repository.SharesI
	billing    BillingI
	storageDir string
	cryptKey   [32]byte
	uploads    chan struct{}
	createMu   sync.Mutex
	sessionsMu sync.Mutex
	sessions   map[string]*shareUploadSession
}

// shareUploadSession is a chunked upload in progress. Sessions live in memory:
// a backend restart drops them and the dashboard asks the user to retry.
type shareUploadSession struct {
	mu           sync.Mutex
	id           string
	userID       string
	filename     string
	mimeType     string
	passwordHash string
	tempPath     string
	size         int64
	received     int64
	expiresAt    time.Time
	direct       bool
	touched      time.Time
	done         bool
}

func NewSharesService(cfg *config.Config, repo repository.SharesI, billing BillingI) SharesI {
	secret := cfg.ShareSigningKey
	if secret == "" {
		secret = "goport-development-share-key-change-in-production"
	}
	service := &sharesService{
		cfg: cfg, repo: repo, billing: billing, storageDir: cfg.ShareStorageDir,
		cryptKey: sha256.Sum256([]byte(secret)), uploads: make(chan struct{}, 2),
		sessions: make(map[string]*shareUploadSession),
	}
	_ = os.MkdirAll(service.storageDir, 0o700)
	service.removeStaleUploadFiles()
	free, pro := service.planLimits()
	log.Printf("share limits: total %d bytes; free %+v; pro %+v", cfg.ShareTotalStorageBytes, free, pro)
	go service.cleanupLoop()
	return service
}

func (s *sharesService) ChunkSize() int64 { return shareChunkSize }

// Limits returns the caller's cloud-file limits along with both plans.
func (s *sharesService) Limits(userID string) (model.ShareLimits, error) {
	plan, err := s.billing.Plan(userID)
	if err != nil {
		return model.ShareLimits{}, err
	}
	free, pro := s.planLimits()
	limits := model.ShareLimits{IsPro: plan.IsPro, Current: free, Free: free, Pro: pro}
	if plan.IsPro {
		limits.Current = pro
	}
	return limits, nil
}

func (s *sharesService) planLimits() (model.SharePlanLimits, model.SharePlanLimits) {
	free := model.SharePlanLimits{
		StorageBytes:          s.cfg.ShareFreeStorageBytes,
		MaxUploadBytes:        s.cfg.ShareFreeMaxUploadBytes,
		DefaultRetentionHours: s.cfg.ShareFreeDefaultRetentionHours,
		MaxRetentionHours:     s.cfg.ShareFreeMaxRetentionHours,
		MaxDownloads:          max(s.cfg.ShareFreeMaxDownloads, 0),
	}
	pro := model.SharePlanLimits{
		StorageBytes:          s.cfg.ShareProStorageBytes,
		MaxUploadBytes:        max(s.cfg.ShareProMaxUploadBytes, s.cfg.ShareFreeMaxUploadBytes),
		DefaultRetentionHours: s.cfg.ShareProDefaultRetentionHours,
		MaxRetentionHours:     max(s.cfg.ShareProMaxRetentionHours, s.cfg.ShareFreeMaxRetentionHours),
		DirectLinks:           true,
	}
	return free, pro
}

// Create stores a file sent in one request. The dashboard uploads in chunks
// instead; this path remains for small files and older clients.
func (s *sharesService) Create(userID, filename string, source io.Reader, expiresAt time.Time, password string, direct bool) (*model.ShareItem, error) {
	select {
	case s.uploads <- struct{}{}:
		defer func() { <-s.uploads }()
	default:
		return nil, ErrShareBusy
	}
	filename = sanitizeFilename(filename)
	if filename == "" || !allowedUpload(filename) {
		return nil, ErrShareType
	}
	limits, err := s.Limits(userID)
	if err != nil {
		return nil, err
	}
	if err := checkDirect(direct, password, limits); err != nil {
		return nil, err
	}
	if expiresAt.IsZero() {
		expiresAt = defaultExpiry(limits.Current)
	}
	if err := validateExpiry(expiresAt, limits.Current); err != nil {
		return nil, err
	}
	passwordHash, err := hashOptionalPassword(password)
	if err != nil {
		return nil, err
	}

	temporary, err := os.CreateTemp(s.storageDir, ".upload-*")
	if err != nil {
		return nil, err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	reader := bufio.NewReader(source)
	sniff, _ := reader.Peek(512)
	mimeType := detectShareType(filename, sniff)
	if !allowedMIME(filename, mimeType) {
		temporary.Close()
		return nil, ErrShareType
	}
	maxBytes := limits.Current.MaxUploadBytes
	written, copyErr := io.Copy(temporary, io.LimitReader(reader, maxBytes+1))
	closeErr := temporary.Close()
	if copyErr != nil {
		return nil, copyErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if written > maxBytes {
		return nil, ErrShareTooLarge
	}
	if written == 0 {
		return nil, ErrShareEmpty
	}
	return s.finalize(userID, temporaryName, written, model.ShareRecord{
		Filename: filename, MIMEType: mimeType, ExpiresAt: expiresAt, PasswordHash: passwordHash, Direct: direct,
	}, limits)
}

// StartUpload opens a chunked upload after checking the plan, the quota and
// the file type, so a doomed upload fails before any bytes are sent.
func (s *sharesService) StartUpload(userID string, request model.StartShareUploadRequest, expiresAt time.Time) (*model.ShareUploadSession, error) {
	filename := sanitizeFilename(request.Filename)
	if filename == "" || !allowedUpload(filename) {
		return nil, ErrShareType
	}
	if request.Size <= 0 {
		return nil, ErrShareEmpty
	}
	limits, err := s.Limits(userID)
	if err != nil {
		return nil, err
	}
	if request.Size > limits.Current.MaxUploadBytes {
		return nil, ErrShareTooLarge
	}
	if err := checkDirect(request.Direct, request.Password, limits); err != nil {
		return nil, err
	}
	if expiresAt.IsZero() {
		expiresAt = defaultExpiry(limits.Current)
	}
	if err := validateExpiry(expiresAt, limits.Current); err != nil {
		return nil, err
	}
	passwordHash, err := hashOptionalPassword(request.Password)
	if err != nil {
		return nil, err
	}
	used, err := s.repo.StorageUsed(userID)
	if err != nil {
		return nil, err
	}
	total, err := s.repo.TotalStorageUsed()
	if err != nil {
		return nil, err
	}
	id, err := randomHex(16)
	if err != nil {
		return nil, err
	}

	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	s.pruneUploadsLocked(time.Now())
	userUploads, reserved, reservedTotal := 0, int64(0), int64(0)
	for _, session := range s.sessions {
		reservedTotal += session.size
		if session.userID == userID {
			userUploads++
			reserved += session.size
		}
	}
	if userUploads >= shareMaxUserUploads || len(s.sessions) >= shareMaxUploads {
		return nil, ErrShareBusy
	}
	if used+reserved+request.Size > limits.Current.StorageBytes {
		return nil, ErrShareQuota
	}
	if s.cfg.ShareTotalStorageBytes > 0 && total+reservedTotal+request.Size > s.cfg.ShareTotalStorageBytes {
		return nil, ErrShareFull
	}
	temporary, err := os.CreateTemp(s.storageDir, ".upload-*")
	if err != nil {
		return nil, err
	}
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporary.Name())
		return nil, err
	}
	s.sessions[id] = &shareUploadSession{
		id: id, userID: userID, filename: filename, passwordHash: passwordHash, tempPath: temporary.Name(),
		size: request.Size, expiresAt: expiresAt, direct: request.Direct, touched: time.Now(),
	}
	return &model.ShareUploadSession{ID: id, ChunkSize: shareChunkSize}, nil
}

// WriteUploadChunk stores bytes starting at offset. Sending a chunk again from
// an earlier offset replaces what was stored after it, so a client can retry a
// chunk whose response it never received.
func (s *sharesService) WriteUploadChunk(userID, uploadID string, offset int64, body io.Reader) (int64, error) {
	session, err := s.uploadSession(userID, uploadID)
	if err != nil {
		return 0, err
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.done {
		return 0, ErrShareUploadNotFound
	}
	if offset < 0 || offset > session.received || offset >= session.size {
		return session.received, &UploadOffsetError{Received: session.received}
	}
	limit := min(session.size-offset, shareChunkSize)

	file, err := os.OpenFile(session.tempPath, os.O_WRONLY, 0o600)
	if err != nil {
		return session.received, err
	}
	defer file.Close()
	rewind := func() {
		_ = file.Truncate(offset)
		session.received = offset
	}
	rewind()
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return session.received, err
	}
	reader := bufio.NewReader(io.LimitReader(body, limit+1))
	if offset == 0 {
		sniff, _ := reader.Peek(512)
		mimeType := detectShareType(session.filename, sniff)
		if !allowedMIME(session.filename, mimeType) {
			s.dropUpload(session)
			return 0, ErrShareType
		}
		session.mimeType = mimeType
	}
	written, err := io.Copy(file, reader)
	if err != nil {
		rewind()
		return session.received, err
	}
	if written > limit {
		rewind()
		return session.received, ErrShareTooLarge
	}
	session.received = offset + written
	session.touched = time.Now()
	return session.received, nil
}

// CompleteUpload turns a fully received upload into a share.
func (s *sharesService) CompleteUpload(userID, uploadID string) (*model.ShareItem, error) {
	session, err := s.uploadSession(userID, uploadID)
	if err != nil {
		return nil, err
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.done {
		return nil, ErrShareUploadNotFound
	}
	if session.received != session.size || session.mimeType == "" {
		return nil, ErrShareUploadIncomplete
	}
	// Every outcome from here ends the session: success moves the file, and a
	// failure (quota, plan change) is not something a retry would fix.
	defer s.dropUpload(session)
	limits, err := s.Limits(userID)
	if err != nil {
		return nil, err
	}
	if session.direct && !limits.Current.DirectLinks {
		return nil, ErrShareDirectPro
	}
	return s.finalize(userID, session.tempPath, session.size, model.ShareRecord{
		Filename: session.filename, MIMEType: session.mimeType, ExpiresAt: session.expiresAt,
		PasswordHash: session.passwordHash, Direct: session.direct,
	}, limits)
}

func (s *sharesService) CancelUpload(userID, uploadID string) error {
	session, err := s.uploadSession(userID, uploadID)
	if err != nil {
		return err
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	s.dropUpload(session)
	return nil
}

func (s *sharesService) uploadSession(userID, uploadID string) (*shareUploadSession, error) {
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	session, ok := s.sessions[uploadID]
	if !ok || session.userID != userID {
		return nil, ErrShareUploadNotFound
	}
	return session, nil
}

// dropUpload removes a session and its temporary file. The caller holds
// session.mu.
func (s *sharesService) dropUpload(session *shareUploadSession) {
	if session.done {
		return
	}
	session.done = true
	_ = os.Remove(session.tempPath)
	s.sessionsMu.Lock()
	delete(s.sessions, session.id)
	s.sessionsMu.Unlock()
}

// pruneUploadsLocked drops sessions idle for too long. The caller holds
// sessionsMu; sessions busy writing a chunk are skipped.
func (s *sharesService) pruneUploadsLocked(now time.Time) {
	for id, session := range s.sessions {
		if !session.mu.TryLock() {
			continue
		}
		if now.Sub(session.touched) > shareUploadIdleTimeout {
			session.done = true
			_ = os.Remove(session.tempPath)
			delete(s.sessions, id)
		}
		session.mu.Unlock()
	}
}

// finalize moves a completed temporary file into storage and records it.
func (s *sharesService) finalize(userID, temporaryPath string, size int64, value model.ShareRecord, limits model.ShareLimits) (*model.ShareItem, error) {
	token, err := randomURLToken(32)
	if err != nil {
		return nil, err
	}
	storageKey, err := randomHex(24)
	if err != nil {
		return nil, err
	}
	tokenCipher, err := s.encryptToken(token)
	if err != nil {
		return nil, err
	}

	s.createMu.Lock()
	defer s.createMu.Unlock()
	used, err := s.repo.StorageUsed(userID)
	if err != nil {
		return nil, err
	}
	if used+size > limits.Current.StorageBytes {
		return nil, ErrShareQuota
	}
	if s.cfg.ShareTotalStorageBytes > 0 {
		total, err := s.repo.TotalStorageUsed()
		if err != nil {
			return nil, err
		}
		if total+size > s.cfg.ShareTotalStorageBytes {
			return nil, ErrShareFull
		}
	}
	finalPath := filepath.Join(s.storageDir, storageKey)
	if err := os.Rename(temporaryPath, finalPath); err != nil {
		return nil, err
	}
	value.OwnerID = userID
	value.TokenHash = tokenHash(token)
	value.TokenCipher = tokenCipher
	value.StorageKey = storageKey
	value.Size = size
	record, err := s.repo.Create(value)
	if err != nil {
		_ = os.Remove(finalPath)
		return nil, err
	}
	item := s.item(*record, token, &limits)
	return &item, nil
}

func (s *sharesService) List(userID string) ([]model.ShareItem, model.ShareStorageUsage, model.ShareLimits, error) {
	records, err := s.repo.ListOwned(userID)
	if err != nil {
		return nil, model.ShareStorageUsage{}, model.ShareLimits{}, err
	}
	limits, err := s.Limits(userID)
	if err != nil {
		return nil, model.ShareStorageUsage{}, model.ShareLimits{}, err
	}
	items := make([]model.ShareItem, 0, len(records))
	for _, record := range records {
		token, decryptErr := s.decryptToken(record.TokenCipher)
		if decryptErr != nil {
			token = ""
		}
		items = append(items, s.item(record, token, &limits))
	}
	used, err := s.repo.StorageUsed(userID)
	if err != nil {
		return nil, model.ShareStorageUsage{}, model.ShareLimits{}, err
	}
	quota := limits.Current.StorageBytes
	return items, model.ShareStorageUsage{UsedBytes: used, QuotaBytes: quota, RemainingBytes: max(quota-used, 0)}, limits, nil
}

func (s *sharesService) Update(userID, id string, expiresAt time.Time, password *string, direct *bool) (*model.ShareItem, error) {
	record, err := s.repo.GetOwned(userID, id)
	if err != nil {
		return nil, ErrShareNotFound
	}
	limits, err := s.Limits(userID)
	if err != nil {
		return nil, err
	}
	if err := validateExpiry(expiresAt, limits.Current); err != nil {
		return nil, err
	}
	if direct != nil && *direct && !record.Direct && !limits.Current.DirectLinks {
		return nil, ErrShareDirectPro
	}
	finalDirect := record.Direct
	if direct != nil {
		finalDirect = *direct
	}
	hasPassword := record.PasswordHash != ""
	var passwordHash *string
	if password != nil {
		hashed, hashErr := hashOptionalPassword(*password)
		if hashErr != nil {
			return nil, hashErr
		}
		passwordHash = &hashed
		hasPassword = hashed != ""
	}
	if finalDirect && hasPassword {
		return nil, ErrShareDirectPassword
	}
	record, err = s.repo.Update(record.ID, expiresAt, passwordHash, direct)
	if err != nil {
		return nil, err
	}
	token, _ := s.decryptToken(record.TokenCipher)
	item := s.item(*record, token, &limits)
	return &item, nil
}

func (s *sharesService) Revoke(userID, id string) error {
	record, err := s.repo.GetOwned(userID, id)
	if err != nil {
		return ErrShareNotFound
	}
	return s.repo.SetRevoked(record.ID, true)
}

// Restore re-enables a revoked link with the same URL.
func (s *sharesService) Restore(userID, id string) error {
	record, err := s.repo.GetOwned(userID, id)
	if err != nil {
		return ErrShareNotFound
	}
	if !time.Now().Before(record.ExpiresAt) {
		return ErrShareExpired
	}
	return s.repo.SetRevoked(record.ID, false)
}

func (s *sharesService) Delete(userID, id string) error {
	record, err := s.repo.GetOwned(userID, id)
	if err != nil {
		return ErrShareNotFound
	}
	if err := os.Remove(filepath.Join(s.storageDir, record.StorageKey)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return s.repo.Delete(record.ID)
}

func (s *sharesService) Public(token string) (*model.ShareItem, *model.ShareRecord, error) {
	if len(token) < 32 {
		return nil, nil, ErrShareNotFound
	}
	record, err := s.repo.GetByTokenHash(tokenHash(token))
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, ErrShareNotFound) {
		return nil, nil, ErrShareNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	if record.Revoked {
		return nil, record, ErrShareRevoked
	}
	if !time.Now().Before(record.ExpiresAt) {
		return nil, record, ErrShareExpired
	}
	if s.downloadLimitReached(record) {
		return nil, record, ErrShareDownloadLimit
	}
	item := s.item(*record, "", nil)
	return &item, record, nil
}

func (s *sharesService) VerifyPassword(token, password string) (*model.ShareRecord, error) {
	_, record, err := s.Public(token)
	if err != nil {
		return nil, err
	}
	if record.PasswordHash == "" {
		return record, nil
	}
	if bcrypt.CompareHashAndPassword([]byte(record.PasswordHash), []byte(password)) != nil {
		return nil, ErrSharePassword
	}
	return record, nil
}

func (s *sharesService) Open(token string) (*os.File, *model.ShareRecord, error) {
	_, record, err := s.Public(token)
	if err != nil {
		return nil, record, err
	}
	file, err := os.Open(filepath.Join(s.storageDir, record.StorageKey))
	if errors.Is(err, os.ErrNotExist) {
		return nil, record, ErrShareNotFound
	}
	return file, record, err
}

// DirectAllowed reports whether a share may be served as the bare file. The
// owner must still be on Pro; otherwise the link falls back to the share page.
func (s *sharesService) DirectAllowed(record *model.ShareRecord) bool {
	if record == nil || !record.Direct || record.PasswordHash != "" {
		return false
	}
	plan, err := s.billing.Plan(record.OwnerID)
	return err == nil && plan.IsPro
}

func (s *sharesService) CountDownload(id string) { _ = s.repo.AddDownload(id) }

// downloadLimitReached reports whether a Free owner's file has used up its
// downloads. Pro files have no limit. The plan is looked up only once the
// count is high enough to matter.
func (s *sharesService) downloadLimitReached(record *model.ShareRecord) bool {
	free, _ := s.planLimits()
	if free.MaxDownloads <= 0 || record.Downloads < free.MaxDownloads {
		return false
	}
	plan, err := s.billing.Plan(record.OwnerID)
	return err != nil || !plan.IsPro
}

func defaultExpiry(limits model.SharePlanLimits) time.Time {
	return time.Now().Add(time.Duration(limits.DefaultRetentionHours) * time.Hour)
}

// ErrShareExpiry explains an expiry time the plan does not allow.
type ErrShareExpiry struct{ Message string }

func (e *ErrShareExpiry) Error() string { return e.Message }

func validateExpiry(value time.Time, limits model.SharePlanLimits) error {
	now := time.Now()
	if !value.After(now) {
		return &ErrShareExpiry{Message: "Choose an expiry time in the future."}
	}
	// A minute of slack so a preset picked in the browser is not refused for
	// the time the request took.
	if value.After(now.Add(time.Duration(limits.MaxRetentionHours)*time.Hour + time.Minute)) {
		return &ErrShareExpiry{Message: "Your plan keeps files for up to " + retentionText(limits.MaxRetentionHours) + "."}
	}
	return nil
}

func retentionText(hours int) string {
	switch {
	case hours == 24:
		return "1 day"
	case hours%24 == 0:
		return fmt.Sprintf("%d days", hours/24)
	case hours == 1:
		return "1 hour"
	default:
		return fmt.Sprintf("%d hours", hours)
	}
}

func checkDirect(direct bool, password string, limits model.ShareLimits) error {
	if !direct {
		return nil
	}
	if !limits.Current.DirectLinks {
		return ErrShareDirectPro
	}
	if strings.TrimSpace(password) != "" {
		return ErrShareDirectPassword
	}
	return nil
}

// item builds the API view of a share. limits are the owner's, or nil on the
// public page where they are not needed.
func (s *sharesService) item(record model.ShareRecord, token string, limits *model.ShareLimits) model.ShareItem {
	directActive := limits != nil && limits.Current.DirectLinks
	downloadLimit := int64(0)
	if limits != nil {
		downloadLimit = limits.Current.MaxDownloads
	}
	direct := record.Direct && directActive && record.PasswordHash == ""
	link := ""
	if token != "" {
		base := strings.TrimRight(s.cfg.AppURL, "/")
		if direct {
			link = base + "/f/" + token + "/" + url.PathEscape(linkFilename(record.Filename))
		} else {
			link = base + "/s/" + token
		}
	}
	return model.ShareItem{
		ID: record.ID, URL: link, Filename: record.Filename, MIMEType: record.MIMEType,
		Size: record.Size, ExpiresAt: record.ExpiresAt.UTC().Format(time.RFC3339),
		Revoked: record.Revoked, PasswordProtected: record.PasswordHash != "", Direct: direct,
		Downloads: record.Downloads, DownloadLimit: downloadLimit, Created: record.Created.UTC().Format(time.RFC3339),
		Previewable: safePreview(record.MIMEType),
	}
}

// linkFilename turns a filename into a readable ASCII URL segment, for
// example "AI kartochka — reja.pdf" becomes "AI-kartochka-reja.pdf". It is
// cosmetic: the server ignores it and sends the real name in
// Content-Disposition.
func linkFilename(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	base := strings.TrimSuffix(name, filepath.Ext(name))
	var out strings.Builder
	dash := false
	for _, r := range base {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			out.WriteRune(r)
			dash = false
		} else if !dash && out.Len() > 0 {
			out.WriteByte('-')
			dash = true
		}
	}
	slug := strings.Trim(out.String(), "-")
	if len(slug) > 80 {
		slug = strings.Trim(slug[:80], "-")
	}
	if slug == "" {
		slug = "file"
	}
	cleanExt := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' {
			return r
		}
		return -1
	}, ext)
	if len(cleanExt) > 1 && len(cleanExt) <= 12 {
		return slug + cleanExt
	}
	return slug
}

func (s *sharesService) CleanupExpired() {
	for {
		records, err := s.repo.Expired(time.Now(), 100)
		if err != nil || len(records) == 0 {
			return
		}
		for _, record := range records {
			_ = os.Remove(filepath.Join(s.storageDir, record.StorageKey))
			_ = s.repo.Delete(record.ID)
		}
	}
}

func (s *sharesService) cleanupLoop() {
	s.CleanupExpired()
	expired := time.NewTicker(time.Hour)
	uploads := time.NewTicker(5 * time.Minute)
	defer expired.Stop()
	defer uploads.Stop()
	for {
		select {
		case <-expired.C:
			s.CleanupExpired()
		case now := <-uploads.C:
			s.sessionsMu.Lock()
			s.pruneUploadsLocked(now)
			s.sessionsMu.Unlock()
		}
	}
}

// removeStaleUploadFiles deletes partial uploads left by a previous run.
func (s *sharesService) removeStaleUploadFiles() {
	matches, _ := filepath.Glob(filepath.Join(s.storageDir, ".upload-*"))
	for _, path := range matches {
		_ = os.Remove(path)
	}
}

func (s *sharesService) encryptToken(token string) (string, error) {
	block, err := aes.NewCipher(s.cryptKey[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(token), nil)
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

func (s *sharesService) decryptToken(value string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(s.cryptKey[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil || len(raw) < gcm.NonceSize() {
		return "", errors.New("invalid encrypted token")
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
	return string(plain), err
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
func randomURLToken(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}
func randomHex(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func hashOptionalPassword(password string) (string, error) {
	password = strings.TrimSpace(password)
	if password == "" {
		return "", nil
	}
	if len(password) < 8 {
		return "", fmt.Errorf("password must contain at least 8 characters")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(hash), err
}

func sanitizeFilename(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if r < 32 || r == '\u007f' {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if len([]rune(name)) > 255 {
		name = string([]rune(name)[:255])
	}
	return name
}

func allowedUpload(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".svg", ".js", ".mjs", ".exe", ".dll", ".com", ".bat", ".cmd", ".ps1", ".sh", ".msi", ".apk":
		return false
	}
	return filepath.Ext(name) != ""
}

func isHTMLName(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".html", ".htm":
		return true
	}
	return false
}

// detectShareType sniffs a file's type. An .html file is always stored as
// HTML when its content is text, since a page that starts with an unusual tag
// sniffs as plain text.
func detectShareType(name string, head []byte) string {
	sniffed := http.DetectContentType(head)
	if isHTMLName(name) && strings.HasPrefix(sniffed, "text/") {
		return "text/html; charset=utf-8"
	}
	return sniffed
}

// allowedMIME refuses active formats. HTML is accepted only under an .html
// or .htm name, because it is served in a sandbox (see isHTMLType); HTML
// disguised as another type would bypass that.
func allowedMIME(name, value string) bool {
	if !allowedUpload(name) {
		return false
	}
	lower := strings.ToLower(value)
	if strings.Contains(lower, "text/html") {
		return isHTMLName(name)
	}
	return !strings.Contains(lower, "javascript") && !strings.Contains(lower, "svg") && !strings.Contains(lower, "executable")
}

// IsHTMLType reports whether a stored type is a web page, which must only
// ever be served inline with the sandbox policy.
func IsHTMLType(value string) bool {
	return strings.HasPrefix(strings.ToLower(value), "text/html")
}

func safePreview(value string) bool {
	return strings.HasPrefix(value, "image/png") || strings.HasPrefix(value, "image/jpeg") ||
		strings.HasPrefix(value, "image/gif") || strings.HasPrefix(value, "image/webp") ||
		strings.HasPrefix(value, "application/pdf") || strings.HasPrefix(value, "text/plain") ||
		IsHTMLType(value)
}
