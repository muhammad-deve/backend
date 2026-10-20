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
	"net/http"
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
	ErrShareNotFound = errors.New("share not found")
	ErrShareExpired  = errors.New("share expired")
	ErrShareRevoked  = errors.New("share revoked")
	ErrSharePassword = errors.New("invalid share password")
	ErrShareQuota    = errors.New("storage quota exceeded")
	ErrShareTooLarge = errors.New("file is too large")
	ErrShareType     = errors.New("file type is not allowed")
)

type SharesI interface {
	Create(userID, filename string, source io.Reader, expiresAt time.Time, password string) (*model.ShareItem, error)
	List(userID string) ([]model.ShareItem, model.ShareStorageUsage, error)
	Update(userID, id string, expiresAt time.Time, password *string) (*model.ShareItem, error)
	Revoke(userID, id string) error
	Delete(userID, id string) error
	Public(token string) (*model.ShareItem, *model.ShareRecord, error)
	VerifyPassword(token, password string) (*model.ShareRecord, error)
	Open(token string) (*os.File, *model.ShareRecord, error)
	CountDownload(id string)
	MaxUploadBytes() int64
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
}

func NewSharesService(cfg *config.Config, repo repository.SharesI, billing BillingI) SharesI {
	secret := cfg.ShareSigningKey
	if secret == "" {
		secret = "goport-development-share-key-change-in-production"
	}
	service := &sharesService{
		cfg: cfg, repo: repo, billing: billing, storageDir: cfg.ShareStorageDir,
		cryptKey: sha256.Sum256([]byte(secret)), uploads: make(chan struct{}, 2),
	}
	_ = os.MkdirAll(service.storageDir, 0o700)
	go service.cleanupLoop()
	return service
}

func (s *sharesService) MaxUploadBytes() int64 { return s.cfg.ShareMaxUploadBytes }

func (s *sharesService) Create(userID, filename string, source io.Reader, expiresAt time.Time, password string) (*model.ShareItem, error) {
	select {
	case s.uploads <- struct{}{}:
		defer func() { <-s.uploads }()
	default:
		return nil, fmt.Errorf("too many uploads in progress")
	}
	filename = sanitizeFilename(filename)
	if filename == "" || !allowedUpload(filename) {
		return nil, ErrShareType
	}
	if expiresAt.IsZero() {
		expiresAt = time.Now().Add(time.Duration(s.cfg.ShareDefaultRetentionHours) * time.Hour)
	}
	if err := s.validateExpiry(expiresAt); err != nil {
		return nil, err
	}
	passwordHash, err := hashOptionalPassword(password)
	if err != nil {
		return nil, err
	}
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

	temporary, err := os.CreateTemp(s.storageDir, ".upload-*")
	if err != nil {
		return nil, err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	reader := bufio.NewReader(source)
	sniff, _ := reader.Peek(512)
	mimeType := http.DetectContentType(sniff)
	if !allowedMIME(filename, mimeType) {
		temporary.Close()
		return nil, ErrShareType
	}
	written, copyErr := io.Copy(temporary, io.LimitReader(reader, s.cfg.ShareMaxUploadBytes+1))
	closeErr := temporary.Close()
	if copyErr != nil {
		return nil, copyErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if written > s.cfg.ShareMaxUploadBytes {
		return nil, ErrShareTooLarge
	}

	s.createMu.Lock()
	defer s.createMu.Unlock()
	used, err := s.repo.StorageUsed(userID)
	if err != nil {
		return nil, err
	}
	quota, err := s.quota(userID)
	if err != nil {
		return nil, err
	}
	if used+written > quota {
		return nil, ErrShareQuota
	}
	finalPath := filepath.Join(s.storageDir, storageKey)
	if err := os.Rename(temporaryName, finalPath); err != nil {
		return nil, err
	}
	record, err := s.repo.Create(model.ShareRecord{
		OwnerID: userID, TokenHash: tokenHash(token), TokenCipher: tokenCipher,
		StorageKey: storageKey, Filename: filename, MIMEType: mimeType, Size: written,
		ExpiresAt: expiresAt, PasswordHash: passwordHash,
	})
	if err != nil {
		_ = os.Remove(finalPath)
		return nil, err
	}
	item := s.item(*record, token)
	return &item, nil
}

func (s *sharesService) List(userID string) ([]model.ShareItem, model.ShareStorageUsage, error) {
	records, err := s.repo.ListOwned(userID)
	if err != nil {
		return nil, model.ShareStorageUsage{}, err
	}
	items := make([]model.ShareItem, 0, len(records))
	for _, record := range records {
		token, decryptErr := s.decryptToken(record.TokenCipher)
		if decryptErr != nil {
			token = ""
		}
		items = append(items, s.item(record, token))
	}
	used, err := s.repo.StorageUsed(userID)
	if err != nil {
		return nil, model.ShareStorageUsage{}, err
	}
	quota, err := s.quota(userID)
	if err != nil {
		return nil, model.ShareStorageUsage{}, err
	}
	return items, model.ShareStorageUsage{UsedBytes: used, QuotaBytes: quota, RemainingBytes: max(quota-used, 0)}, nil
}

func (s *sharesService) Update(userID, id string, expiresAt time.Time, password *string) (*model.ShareItem, error) {
	record, err := s.repo.GetOwned(userID, id)
	if err != nil {
		return nil, ErrShareNotFound
	}
	if err := s.validateExpiry(expiresAt); err != nil {
		return nil, err
	}
	var passwordHash *string
	if password != nil {
		hashed, hashErr := hashOptionalPassword(*password)
		if hashErr != nil {
			return nil, hashErr
		}
		passwordHash = &hashed
	}
	record, err = s.repo.Update(record.ID, expiresAt, passwordHash)
	if err != nil {
		return nil, err
	}
	token, _ := s.decryptToken(record.TokenCipher)
	item := s.item(*record, token)
	return &item, nil
}

func (s *sharesService) Revoke(userID, id string) error {
	record, err := s.repo.GetOwned(userID, id)
	if err != nil {
		return ErrShareNotFound
	}
	return s.repo.SetRevoked(record.ID, true)
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
	if errors.Is(err, sql.ErrNoRows) {
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
	item := s.item(*record, "")
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

func (s *sharesService) CountDownload(id string) { _ = s.repo.AddDownload(id) }

func (s *sharesService) quota(userID string) (int64, error) {
	plan, err := s.billing.Plan(userID)
	if err != nil {
		return 0, err
	}
	if plan.IsPro {
		return s.cfg.ShareProStorageBytes, nil
	}
	return s.cfg.ShareFreeStorageBytes, nil
}

func (s *sharesService) validateExpiry(value time.Time) error {
	now := time.Now()
	if !value.After(now) {
		return fmt.Errorf("expiration must be in the future")
	}
	if value.After(now.Add(time.Duration(s.cfg.ShareMaxRetentionHours) * time.Hour)) {
		return fmt.Errorf("expiration exceeds the maximum retention period")
	}
	return nil
}

func (s *sharesService) item(record model.ShareRecord, token string) model.ShareItem {
	url := ""
	if token != "" {
		url = strings.TrimRight(s.cfg.AppURL, "/") + "/s/" + token
	}
	return model.ShareItem{
		ID: record.ID, URL: url, Filename: record.Filename, MIMEType: record.MIMEType,
		Size: record.Size, ExpiresAt: record.ExpiresAt.UTC().Format(time.RFC3339),
		Revoked: record.Revoked, PasswordProtected: record.PasswordHash != "",
		Downloads: record.Downloads, Created: record.Created.UTC().Format(time.RFC3339),
		Previewable: safePreview(record.MIMEType),
	}
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
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		s.CleanupExpired()
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
	case ".html", ".htm", ".svg", ".js", ".mjs", ".exe", ".dll", ".com", ".bat", ".cmd", ".ps1", ".sh", ".msi", ".apk":
		return false
	}
	return filepath.Ext(name) != ""
}

func allowedMIME(name, value string) bool {
	if !allowedUpload(name) {
		return false
	}
	lower := strings.ToLower(value)
	return !strings.Contains(lower, "text/html") && !strings.Contains(lower, "javascript") && !strings.Contains(lower, "svg") && !strings.Contains(lower, "executable")
}

func safePreview(value string) bool {
	return strings.HasPrefix(value, "image/png") || strings.HasPrefix(value, "image/jpeg") ||
		strings.HasPrefix(value, "image/gif") || strings.HasPrefix(value, "image/webp") ||
		strings.HasPrefix(value, "application/pdf") || strings.HasPrefix(value, "text/plain")
}
