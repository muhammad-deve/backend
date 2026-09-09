package sqlite

import (
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/model"
)

type Tunnels struct {
	app core.App
}

func NewTunnels(app core.App) *Tunnels {
	return &Tunnels{app: app}
}

func (r *Tunnels) GetOwned(userID, subdomain string) (*model.TunnelRecord, error) {
	record, err := r.app.FindFirstRecordByFilter(
		model.TunnelsCollection,
		"user = {:user} && subdomain = {:subdomain}",
		dbx.Params{"user": userID, "subdomain": subdomain},
	)
	if err != nil {
		return nil, err
	}
	return &model.TunnelRecord{
		ID:        record.Id,
		Subdomain: record.GetString("subdomain"),
		IsCurrent: record.GetBool("is_current"),
	}, nil
}

func (r *Tunnels) DeleteWithLogs(tunnelID string) error {
	logs, err := r.app.FindRecordsByFilter(
		model.TunnelLogsCollection,
		"tunnel_id = {:tunnel}",
		"",
		0,
		0,
		dbx.Params{"tunnel": tunnelID},
	)
	if err != nil {
		return err
	}
	for _, item := range logs {
		if err := r.app.Delete(item); err != nil {
			return err
		}
	}
	record, err := r.app.FindRecordById(model.TunnelsCollection, tunnelID)
	if err != nil {
		return err
	}
	return r.app.Delete(record)
}
