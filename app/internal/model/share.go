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
	Downloads    int64
	Created      time.Time
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
	Downloads         int64  `json:"downloads"`
	Created           string `json:"created"`
	Previewable       bool   `json:"previewable"`
}

type UpdateShareRequest struct {
	ExpiresAt string  `json:"expiresAt"`
	Password  *string `json:"password"`
}
type ShareStorageUsage struct {
	UsedBytes      int64 `json:"usedBytes"`
	QuotaBytes     int64 `json:"quotaBytes"`
	RemainingBytes int64 `json:"remainingBytes"`
}
