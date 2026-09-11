package model

type TunnelRecord struct {
	ID        string
	Subdomain string
	IsCustom  bool
	IsCurrent bool
	LocalPort string
	Protocol  string
	Created   string
}
