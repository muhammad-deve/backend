package repository

import (
	"github.com/pocketbase/pocketbase/core"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/model"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/repository/sqlite"
)

type AuthorizationI interface {
}

type TunnelsI interface {
	GetOwned(userID, subdomain string) (*model.TunnelRecord, error)
	DeleteWithLogs(tunnelID string) error
}

type I interface {
	Authorization() AuthorizationI
	Tunnels() TunnelsI
}

type repository struct {
	AuthorizationI
	tunnels TunnelsI
}

func (r *repository) Authorization() AuthorizationI {
	return r.AuthorizationI
}

func (r *repository) Tunnels() TunnelsI {
	return r.tunnels
}

func NewRepository(app core.App) I {
	return &repository{
		AuthorizationI: sqlite.NewAuthorization(app.DB()),
		tunnels:        sqlite.NewTunnels(app),
	}
}
