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
	// MonthBytes is traffic used since the first of the current UTC month --
	// the figure the plan's monthly allowance is actually measured against.
	// TotalRequests/TotalBytes above are lifetime figures.
	MonthBytes int64 `json:"monthBytes"`
	// MonthResetsAt is when the monthly allowance rolls over (RFC 3339).
	MonthResetsAt string `json:"monthResetsAt"`
	Domains       []DashboardDomain `json:"domains"`
	Tokens        []TokenItem       `json:"tokens"`
	Billing       *BillingData      `json:"billing"`
}

// DashboardDomain describes a single tunnel/subdomain owned by the user along
// with its lifetime traffic stats.
type DashboardDomain struct {
	Subdomain  string `json:"subdomain"`
	URL        string `json:"url"`
	IsCustom   bool   `json:"isCustom"`
	IsCurrent  bool   `json:"isCurrent"`
	LocalPort  string `json:"localPort,omitempty"`
	Protocol   string `json:"protocol,omitempty"`
	Requests   int64  `json:"requests"`
	Bytes      int64  `json:"bytes"`
	LastActive string `json:"lastActive,omitempty"`
	Created    string `json:"created,omitempty"`
}
