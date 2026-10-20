package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

const sharesCollectionID = "pbc_3311000301"

func init() {
	m.Register(func(app core.App) error {
		users, err := app.FindCollectionByNameOrId("users")
		if err != nil {
			return err
		}
		collection := core.NewBaseCollection("shares", sharesCollectionID)
		collection.Fields.Add(
			&core.RelationField{Name: "owner", CollectionId: users.Id, CascadeDelete: true, MinSelect: 1, MaxSelect: 1, Required: true},
			&core.TextField{Name: "token_hash", Required: true, Max: 64},
			&core.TextField{Name: "token_cipher", Required: true, Hidden: true, Max: 200},
			&core.TextField{Name: "storage_key", Required: true, Max: 80},
			&core.TextField{Name: "filename", Required: true, Max: 255},
			&core.TextField{Name: "mime_type", Required: true, Max: 150},
			&core.NumberField{Name: "size", Required: true, OnlyInt: true},
			&core.DateField{Name: "expires_at", Required: true},
			&core.BoolField{Name: "revoked"},
			&core.TextField{Name: "password_hash", Hidden: true, Max: 100},
			&core.NumberField{Name: "downloads", OnlyInt: true},
			&core.AutodateField{Name: "created", OnCreate: true},
			&core.AutodateField{Name: "updated", OnCreate: true, OnUpdate: true},
		)
		collection.Indexes = []string{
			"CREATE UNIQUE INDEX idx_shares_token_hash ON shares (token_hash)",
			"CREATE UNIQUE INDEX idx_shares_storage_key ON shares (storage_key)",
			"CREATE INDEX idx_shares_owner_created ON shares (owner, created)",
			"CREATE INDEX idx_shares_expiry ON shares (expires_at, revoked)",
		}
		return app.Save(collection)
	}, func(app core.App) error {
		collection, err := app.FindCollectionByNameOrId(sharesCollectionID)
		if err != nil {
			return err
		}
		return app.Delete(collection)
	})
}
