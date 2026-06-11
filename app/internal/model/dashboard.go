package model

// DashboardResponse is the payload returned by the authenticated dashboard
// endpoint. It bundles everything the landing dashboard renders: the user's
// CLI token, aggregate traffic stats, and the list of subdomains they own.
type DashboardResponse struct {
	Name          string            `json:"name"`
	Email         string            `json:"email"`
	Avatar        string            `json:"avatar"`
	TotalRequests int64             `json:"totalRequests"`
	TotalBytes    int64             `json:"totalBytes"`
	Domains       []DashboardDomain `json:"domains"`
	Tokens        []TokenItem       `json:"tokens"`
}

// DashboardDomain describes a single tunnel/subdomain owned by the user along
// with its lifetime traffic stats.
type DashboardDomain struct {
	Subdomain  string `json:"subdomain"`
	URL        string `json:"url"`
	IsCustom   bool   `json:"isCustom"`
	Requests   int64  `json:"requests"`
	Bytes      int64  `json:"bytes"`
	LastActive string `json:"lastActive,omitempty"`
	Created    string `json:"created,omitempty"`
}
