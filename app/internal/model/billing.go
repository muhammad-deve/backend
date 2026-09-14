package model

import "time"

const (
	PlanFree       = "free"
	PlanPro        = "pro"
	PlanProMonthly = "pro_monthly"
	PlanProYearly  = "pro_yearly"

	FreeMaxActiveTunnels int   = 1
	ProMaxActiveTunnels  int   = 10
	FreeMaxTokens        int   = 1
	ProMaxTokens         int   = 5
	FreeMonthlyBytes     int64 = 5 * 1024 * 1024 * 1024
	ProMonthlyBytes      int64 = 70 * 1024 * 1024 * 1024
)

type PlanLimits struct {
	Key              string `json:"key"`
	IsPro            bool   `json:"isPro"`
	MaxActiveTunnels int    `json:"maxActiveTunnels"`
	MaxTokens        int    `json:"maxTokens"`
	MonthlyBytes     int64  `json:"monthlyBytes"`
	CustomSubdomains bool   `json:"customSubdomains"`
}

func FreePlanLimits() PlanLimits {
	return PlanLimits{
		Key:              PlanFree,
		MaxActiveTunnels: FreeMaxActiveTunnels,
		MaxTokens:        FreeMaxTokens,
		MonthlyBytes:     FreeMonthlyBytes,
	}
}

func ProPlanLimits() PlanLimits {
	return PlanLimits{
		Key:              PlanPro,
		IsPro:            true,
		MaxActiveTunnels: ProMaxActiveTunnels,
		MaxTokens:        ProMaxTokens,
		MonthlyBytes:     ProMonthlyBytes,
		CustomSubdomains: true,
	}
}

type CheckoutRequest struct {
	Plan string `json:"plan" form:"plan"`
}

type CheckoutResponse struct {
	URL string `json:"url"`
}

type ChangePlanRequest struct {
	Plan string `json:"plan" form:"plan"`
}

type BillingSubscriptionRecord struct {
	ID                string
	UserID            string
	ExternalID        string
	CustomerID        string
	OrderID           string
	ProductID         string
	VariantID         string
	PlanKey           string
	ProductName       string
	VariantName       string
	Status            string
	RenewsAt          time.Time
	EndsAt            time.Time
	TrialEndsAt       time.Time
	ProviderUpdatedAt time.Time
	TestMode          bool
}

type BillingTransactionRecord struct {
	ID                     string
	UserID                 string
	ExternalID             string
	ExternalType           string
	ExternalSubscriptionID string
	BillingReason          string
	Description            string
	AmountCents            int64
	RefundedAmountCents    int64
	Currency               string
	Status                 string
	ChargedAt              time.Time
	InvoiceURL             string
	ProviderUpdatedAt      time.Time
	TestMode               bool
}

type BillingData struct {
	IsPro              bool                 `json:"isPro"`
	CheckoutConfigured bool                 `json:"checkoutConfigured"`
	AvailablePlans     []string             `json:"availablePlans"`
	Plan               PlanLimits           `json:"plan"`
	Subscription       *BillingSubscription `json:"subscription"`
	Transactions       []BillingTransaction `json:"transactions"`
}

type BillingSubscription struct {
	ID                string `json:"id"`
	PlanName          string `json:"planName"`
	Status            string `json:"status"`
	AmountCents       int64  `json:"amountCents"`
	Currency          string `json:"currency"`
	Interval          string `json:"interval"`
	CurrentPeriodEnd  string `json:"currentPeriodEnd,omitempty"`
	CancelAtPeriodEnd bool   `json:"cancelAtPeriodEnd"`
}

type BillingTransaction struct {
	ID          string `json:"id"`
	AmountCents int64  `json:"amountCents"`
	Currency    string `json:"currency"`
	Description string `json:"description"`
	Status      string `json:"status"`
	ChargedAt   string `json:"chargedAt"`
	InvoiceURL  string `json:"invoiceUrl,omitempty"`
}
