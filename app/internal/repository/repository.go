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
}

type I interface {
	Authorization() AuthorizationI
	Tunnels() TunnelsI
	Usage() UsageI
}

type repository struct {
	AuthorizationI
	tunnels TunnelsI
	usage   UsageI
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

func NewRepository(app core.App) I {
	return &repository{
		AuthorizationI: sqlite.NewAuthorization(app.DB()),
		tunnels:        sqlite.NewTunnels(app),
		usage:          sqlite.NewUsage(app),
	}
}
