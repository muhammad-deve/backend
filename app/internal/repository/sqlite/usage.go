package sqlite

import (
	"database/sql"
	"errors"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/model"
)

type Usage struct {
	app core.App
}

func NewUsage(app core.App) *Usage {
	return &Usage{app: app}
}

func (r *Usage) AddTraffic(tunnelID string, bucketStart time.Time, delta model.UsageDelta) error {
	bucketStart = bucketStart.UTC().Truncate(model.UsageBucketDuration)
	if delta.LastActive.IsZero() {
		delta.LastActive = bucketStart
	}

	return r.app.RunInTransaction(func(txApp core.App) error {
		if _, err := txApp.FindRecordById(model.TunnelsCollection, tunnelID); err != nil {
			return err
		}
		if err := incrementLifetimeUsage(txApp, tunnelID, delta); err != nil {
			return err
		}
		return incrementBucketUsage(txApp, tunnelID, bucketStart, delta)
	})
}

func (r *Usage) Total(tunnelID string) (model.UsageTotal, error) {
	rows, err := r.app.FindRecordsByFilter(
		model.TunnelLogsCollection,
		"tunnel_id = {:tunnel}",
		"-last_active",
		0,
		0,
		dbx.Params{"tunnel": tunnelID},
	)
	if err != nil {
		return model.UsageTotal{}, err
	}

	var total model.UsageTotal
	for _, row := range rows {
		total.Requests += int64(row.GetFloat("request_count"))
		total.Bytes += int64(row.GetFloat("bytes_transferred"))
		if total.LastActive == "" {
			total.LastActive = row.GetString("last_active")
		}
	}
	return total, nil
}

func (r *Usage) ListBuckets(userID string, from, to time.Time) ([]model.UsageBucketRecord, error) {
	rows, err := r.app.FindRecordsByFilter(
		model.TunnelUsageBucketsCollection,
		"tunnel_id.user = {:user} && bucket_start >= {:from} && bucket_start < {:to}",
		"bucket_start",
		0,
		0,
		dbx.Params{
			"user": userID,
			"from": from.UTC(),
			"to":   to.UTC(),
		},
	)
	if err != nil {
		return nil, err
	}

	buckets := make([]model.UsageBucketRecord, 0, len(rows))
	for _, row := range rows {
		buckets = append(buckets, model.UsageBucketRecord{
			TunnelID:    row.GetString("tunnel_id"),
			BucketStart: row.GetDateTime("bucket_start").Time().UTC(),
			Requests:    int64(row.GetFloat("request_count")),
			Bytes:       int64(row.GetFloat("bytes_transferred")),
		})
	}
	return buckets, nil
}

func (r *Usage) BytesForPeriod(userID string, from, to time.Time) (int64, error) {
	var result struct {
		Bytes int64 `db:"bytes"`
	}
	err := r.app.DB().NewQuery(`
		SELECT COALESCE(SUM(bucket.bytes_transferred), 0) AS bytes
		FROM tunnel_usage_buckets AS bucket
		INNER JOIN tunnels AS tunnel ON tunnel.id = bucket.tunnel_id
		WHERE tunnel.user = {:user}
			AND bucket.bucket_start >= {:from}
			AND bucket.bucket_start < {:to}
	`).Bind(dbx.Params{
		"user": userID,
		"from": from.UTC(),
		"to":   to.UTC(),
	}).One(&result)
	if err != nil {
		return 0, err
	}
	return result.Bytes, nil
}

func incrementLifetimeUsage(app core.App, tunnelID string, delta model.UsageDelta) error {
	collection, err := app.FindCollectionByNameOrId(model.TunnelLogsCollection)
	if err != nil {
		return err
	}

	row, err := app.FindFirstRecordByFilter(
		model.TunnelLogsCollection,
		"tunnel_id = {:tunnel}",
		dbx.Params{"tunnel": tunnelID},
	)
	if errors.Is(err, sql.ErrNoRows) {
		row = core.NewRecord(collection)
		row.Set("tunnel_id", tunnelID)
	} else if err != nil {
		return err
	}

	row.Set("request_count", row.GetFloat("request_count")+float64(delta.Requests))
	row.Set("bytes_transferred", row.GetFloat("bytes_transferred")+float64(delta.Bytes))
	if delta.LastActive.After(row.GetDateTime("last_active").Time()) {
		row.Set("last_active", delta.LastActive.UTC())
	}
	return app.Save(row)
}

func incrementBucketUsage(app core.App, tunnelID string, bucketStart time.Time, delta model.UsageDelta) error {
	collection, err := app.FindCollectionByNameOrId(model.TunnelUsageBucketsCollection)
	if err != nil {
		return err
	}

	row, err := app.FindFirstRecordByFilter(
		model.TunnelUsageBucketsCollection,
		"tunnel_id = {:tunnel} && bucket_start = {:bucket}",
		dbx.Params{"tunnel": tunnelID, "bucket": bucketStart},
	)
	if errors.Is(err, sql.ErrNoRows) {
		row = core.NewRecord(collection)
		row.Set("tunnel_id", tunnelID)
		row.Set("bucket_start", bucketStart)
	} else if err != nil {
		return err
	}

	row.Set("request_count", row.GetFloat("request_count")+float64(delta.Requests))
	row.Set("bytes_transferred", row.GetFloat("bytes_transferred")+float64(delta.Bytes))
	return app.Save(row)
}
