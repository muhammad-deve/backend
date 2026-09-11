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

func (r *Tunnels) DeleteWithLogs(tunnelID string) error {
	for _, collection := range []string{model.TunnelLogsCollection, model.TunnelUsageBucketsCollection} {
		records, err := r.app.FindRecordsByFilter(
			collection,
			"tunnel_id = {:tunnel}",
			"",
			0,
			0,
			dbx.Params{"tunnel": tunnelID},
		)
		if err != nil {
			return err
		}
		for _, record := range records {
			if err := r.app.Delete(record); err != nil {
				return err
			}
		}
	}

	record, err := r.app.FindRecordById(model.TunnelsCollection, tunnelID)
	if err != nil {
		return err
	}
	return r.app.Delete(record)
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
