package repository

import (
	"time"

	"github.com/pocketbase/pocketbase/core"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/model"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/repository/sqlite"
)

type AuthorizationI interface {
}

type TunnelsI interface {
	GetOwned(userID, subdomain string) (*model.TunnelRecord, error)
	ListOwned(userID string) ([]model.TunnelRecord, error)
	DeleteWithLogs(tunnelID string) error
}

type UsageI interface {
	AddTraffic(tunnelID string, bucketStart time.Time, delta model.UsageDelta) error
	Total(tunnelID string) (model.UsageTotal, error)
	ListBuckets(userID string, from, to time.Time) ([]model.UsageBucketRecord, error)
	BytesForPeriod(userID string, from, to time.Time) (int64, error)
}

type BillingI interface {
	UpsertSubscription(subscription model.BillingSubscriptionRecord) error
	ListSubscriptions(userID string) ([]model.BillingSubscriptionRecord, error)
	FindSubscriptionByExternalID(externalID string) (*model.BillingSubscriptionRecord, error)
	UpsertTransaction(transaction model.BillingTransactionRecord) error
	ListTransactions(userID string, limit int) ([]model.BillingTransactionRecord, error)
	UserExists(userID string) (bool, error)
	FindUserIDByEmail(email string) (string, error)
}

type SharesI interface {
	Create(record model.ShareRecord) (*model.ShareRecord, error)
	ListOwned(userID string) ([]model.ShareRecord, error)
	GetOwned(userID, id string) (*model.ShareRecord, error)
	GetByTokenHash(hash string) (*model.ShareRecord, error)
	Update(id string, expiresAt time.Time, passwordHash *string) (*model.ShareRecord, error)
	SetRevoked(id string, revoked bool) error
	Delete(id string) error
	AddDownload(id string) error
	StorageUsed(userID string) (int64, error)
	Expired(before time.Time, limit int) ([]model.ShareRecord, error)
}

type I interface {
	Authorization() AuthorizationI
	Tunnels() TunnelsI
	Usage() UsageI
	Billing() BillingI
	Shares() SharesI
}

type repository struct {
	AuthorizationI
	tunnels TunnelsI
	usage   UsageI
	billing BillingI
	shares  SharesI
}

func (r *repository) Authorization() AuthorizationI {
	return r.AuthorizationI
}

func (r *repository) Tunnels() TunnelsI {
	return r.tunnels
}

func (r *repository) Usage() UsageI {
	return r.usage
}

func (r *repository) Billing() BillingI { return r.billing }
func (r *repository) Shares() SharesI   { return r.shares }

func NewRepository(app core.App) I {
	return &repository{
		AuthorizationI: sqlite.NewAuthorization(app.DB()),
		tunnels:        sqlite.NewTunnels(app),
		usage:          sqlite.NewUsage(app),
		billing:        sqlite.NewBilling(app),
		shares:         sqlite.NewShares(app),
	}
}
