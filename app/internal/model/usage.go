package model

import "time"

const UsageBucketDuration = 5 * time.Minute

type UsageDelta struct {
	Requests   int64
	Bytes      int64
	LastActive time.Time
}

type UsageTotal struct {
	Requests   int64
	Bytes      int64
	LastActive string
}

type UsageBucketRecord struct {
	TunnelID    string
	BucketStart time.Time
	Requests    int64
	Bytes       int64
}

type UsageResponse struct {
	Range         string        `json:"range"`
	From          time.Time     `json:"from"`
	To            time.Time     `json:"to"`
	BucketSeconds int64         `json:"bucketSeconds"`
	Series        []UsageSeries `json:"series"`
}

type UsageSeries struct {
	TunnelID  string       `json:"tunnelId"`
	Subdomain string       `json:"subdomain"`
	URL       string       `json:"url"`
	Points    []UsagePoint `json:"points"`
}

type UsagePoint struct {
	Timestamp time.Time `json:"timestamp"`
	Requests  int64     `json:"requests"`
	Bytes     int64     `json:"bytes"`
}
