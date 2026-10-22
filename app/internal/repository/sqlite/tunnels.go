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
	return tunnelRecord(record), nil
}

func (r *Tunnels) ListOwned(userID string) ([]model.TunnelRecord, error) {
	records, err := r.app.FindRecordsByFilter(
		model.TunnelsCollection,
		"user = {:user}",
		"-updated",
		200,
		0,
		dbx.Params{"user": userID},
	)
	if err != nil {
		return nil, err
	}

	tunnels := make([]model.TunnelRecord, 0, len(records))
	for _, record := range records {
		tunnels = append(tunnels, *tunnelRecord(record))
	}
	return tunnels, nil
}

// DeleteWithLogs deletes a tunnel and its lifetime totals. Its usage buckets
// stay, detached from the tunnel (tunnel_id emptied), because monthly usage
// is per account and must not drop when a tunnel is deleted.
func (r *Tunnels) DeleteWithLogs(tunnelID string) error {
	return r.app.RunInTransaction(func(tx core.App) error {
		record, err := tx.FindRecordById(model.TunnelsCollection, tunnelID)
		if err != nil {
			return err
		}
		logs, err := tx.FindRecordsByFilter(
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
		for _, row := range logs {
			if err := tx.Delete(row); err != nil {
				return err
			}
		}
		_, err = tx.DB().NewQuery(`
			UPDATE tunnel_usage_buckets
			SET [[user]] = CASE WHEN [[user]] = '' THEN {:owner} ELSE [[user]] END,
				tunnel_id = ''
			WHERE tunnel_id = {:tunnel}
		`).Bind(dbx.Params{"owner": record.GetString("user"), "tunnel": tunnelID}).Execute()
		if err != nil {
			return err
		}
		return tx.Delete(record)
	})
}

func tunnelRecord(record *core.Record) *model.TunnelRecord {
	return &model.TunnelRecord{
		ID:        record.Id,
		Subdomain: record.GetString("subdomain"),
		IsCustom:  record.GetBool("is_custom"),
		IsCurrent: record.GetBool("is_current"),
		LocalPort: record.GetString("local_port"),
		Protocol:  record.GetString("protocol"),
		Created:   record.GetString("created"),
	}
}
