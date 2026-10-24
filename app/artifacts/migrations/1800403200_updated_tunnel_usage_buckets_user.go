package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

// Monthly traffic is summed per account, but tunnel_usage_buckets only knew
// its tunnel and was deleted with it, so deleting an offline tunnel reset the
// account's monthly quota.
//
// Buckets now carry their owner in `user`, filled from tunnels, and tunnel_id
// becomes optional without cascade: a deleted tunnel's buckets stay, with an
// empty tunnel_id, and keep counting for the account. The unique
// (tunnel_id, bucket_start) index now covers only buckets that still have a
// tunnel; a plain index on the same columns keeps lookups by tunnel fast,
// since SQLite can't use the partial one for a bound tunnel id.
//
// Safe on live data and safe to run again: the field is added only when
// missing, indexes are replaced by name (the old unique index already
// guarantees the new partial one), and the backfill only fills empty values.
const (
	usageBucketsCollectionID = "pbc_6495136435"
	usageBucketsUserFieldID  = "relation3311000301"
)

func init() {
	m.Register(addUsageBucketOwner, removeUsageBucketOwner)
}

func addUsageBucketOwner(app core.App) error {
	buckets, err := app.FindCollectionByNameOrId(usageBucketsCollectionID)
	if err != nil {
		return err
	}
	users, err := app.FindCollectionByNameOrId("users")
	if err != nil {
		return err
	}

	if buckets.Fields.GetByName("user") == nil {
		buckets.Fields.Add(&core.RelationField{
			Id:           usageBucketsUserFieldID,
			Name:         "user",
			CollectionId: users.Id,
			MaxSelect:    1,
		})
	}
	if tunnel, ok := buckets.Fields.GetByName("tunnel_id").(*core.RelationField); ok {
		tunnel.Required = false
		tunnel.CascadeDelete = false
	}
	buckets.AddIndex("idx_tunnel_usage_bucket", true, "`tunnel_id`, `bucket_start`", "`tunnel_id` != ''")
	buckets.AddIndex("idx_tunnel_usage_lookup", false, "`tunnel_id`, `bucket_start`", "")
	buckets.AddIndex("idx_tunnel_usage_user", false, "`user`, `bucket_start`", "")
	if err := app.Save(buckets); err != nil {
		return err
	}

	_, err = app.DB().NewQuery(`
		UPDATE tunnel_usage_buckets
		SET [[user]] = (SELECT tunnels.[[user]] FROM tunnels WHERE tunnels.id = tunnel_usage_buckets.tunnel_id)
		WHERE ([[user]] = '' OR [[user]] IS NULL)
			AND EXISTS (
				SELECT 1 FROM tunnels
				WHERE tunnels.id = tunnel_usage_buckets.tunnel_id AND tunnels.[[user]] != ''
			)
	`).Execute()
	return err
}

func removeUsageBucketOwner(app core.App) error {
	buckets, err := app.FindCollectionByNameOrId(usageBucketsCollectionID)
	if err != nil {
		return err
	}
	// Buckets of deleted tunnels can't go back under a required relation.
	if _, err := app.DB().NewQuery("DELETE FROM tunnel_usage_buckets WHERE tunnel_id = ''").Execute(); err != nil {
		return err
	}
	buckets.RemoveIndex("idx_tunnel_usage_lookup")
	buckets.RemoveIndex("idx_tunnel_usage_user")
	buckets.AddIndex("idx_tunnel_usage_bucket", true, "`tunnel_id`, `bucket_start`", "")
	buckets.Fields.RemoveByName("user")
	if tunnel, ok := buckets.Fields.GetByName("tunnel_id").(*core.RelationField); ok {
		tunnel.Required = true
		tunnel.CascadeDelete = true
	}
	return app.Save(buckets)
}
