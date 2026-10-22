package model

import "time"

type ShareRecord struct {
	ID           string
	OwnerID      string
	TokenHash    string
	TokenCipher  string
	StorageKey   string
	Filename     string
	MIMEType     string
	Size         int64
	ExpiresAt    time.Time
	Revoked      bool
	PasswordHash string
	// Direct links open the file itself instead of the GoPort share page.
	// They are a Pro feature and never carry a password.
	Direct    bool
	Downloads int64
	Created   time.Time
}

type ShareItem struct {
	ID                string `json:"id,omitempty"`
	URL               string `json:"url,omitempty"`
	Filename          string `json:"filename"`
	MIMEType          string `json:"mimeType"`
	Size              int64  `json:"size"`
	ExpiresAt         string `json:"expiresAt"`
	Revoked           bool   `json:"revoked"`
	PasswordProtected bool   `json:"passwordProtected"`
	Direct            bool   `json:"direct"`
	Downloads         int64  `json:"downloads"`
	// DownloadLimit is how many downloads the owner's plan allows for this
	// file; zero means unlimited.
	DownloadLimit int64  `json:"downloadLimit,omitempty"`
	Created       string `json:"created"`
	Previewable   bool   `json:"previewable"`
}

// PublicShare is what the public share page receives. While a password is
// still needed (Locked), it says only that, and when the link expires.
type PublicShare struct {
	Filename          string `json:"filename,omitempty"`
	MIMEType          string `json:"mimeType,omitempty"`
	Size              int64  `json:"size,omitempty"`
	ExpiresAt         string `json:"expiresAt"`
	PasswordProtected bool   `json:"passwordProtected"`
	Locked            bool   `json:"locked"`
	Previewable       bool   `json:"previewable"`
	// RetryAfter is set when this visitor is locked out of the password
	// form, so a reload keeps showing the countdown.
	RetryAfter int64 `json:"retryAfter,omitempty"`
}

type UpdateShareRequest struct {
	ExpiresAt string  `json:"expiresAt"`
	Password  *string `json:"password"`
	Direct    *bool   `json:"direct"`
}

type ShareStorageUsage struct {
	UsedBytes      int64 `json:"usedBytes"`
	QuotaBytes     int64 `json:"quotaBytes"`
	RemainingBytes int64 `json:"remainingBytes"`
	Files          int64 `json:"files"`
}

// SharePlanLimits describes what one plan allows for cloud files.
type SharePlanLimits struct {
	StorageBytes          int64 `json:"storageBytes"`
	MaxUploadBytes        int64 `json:"maxUploadBytes"`
	DefaultRetentionHours int   `json:"defaultRetentionHours"`
	MaxRetentionHours     int   `json:"maxRetentionHours"`
	// MaxDownloads is per file; zero means unlimited.
	MaxDownloads int64 `json:"maxDownloads"`
	// MaxFiles is how many files one account may keep; zero means unlimited.
	MaxFiles    int64 `json:"maxFiles"`
	DirectLinks bool  `json:"directLinks"`
}

// ShareLimits is the caller's plan plus both plans, so the dashboard can show
// what an upgrade changes.
type ShareLimits struct {
	IsPro   bool            `json:"isPro"`
	Current SharePlanLimits `json:"current"`
	Free    SharePlanLimits `json:"free"`
	Pro     SharePlanLimits `json:"pro"`
}

type StartShareUploadRequest struct {
	Filename  string `json:"filename"`
	Size      int64  `json:"size"`
	ExpiresAt string `json:"expiresAt"`
	Password  string `json:"password"`
	Direct    bool   `json:"direct"`
}

type ShareUploadSession struct {
	ID        string `json:"id"`
	ChunkSize int64  `json:"chunkSize"`
	Received  int64  `json:"received"`
}
