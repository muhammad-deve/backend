package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

// Adds a per-user `api_token` to the users collection. The token is what a
// developer passes to `goport auth <token>` so their tunnels are linked to
// their account. It is hidden from the generic PocketBase API and only ever
// surfaced through the authenticated dashboard endpoint.
func init() {
	m.Register(func(app core.App) error {
		collection, err := app.FindCollectionByNameOrId("users")
		if err != nil {
			return err
		}

		// Skip if it already exists (idempotent).
		if collection.Fields.GetByName("api_token") != nil {
			return nil
		}

		collection.Fields.Add(&core.TextField{
			Id:                  "text_api_token_001",
			Name:                "api_token",
			Max:                 64,
			AutogeneratePattern: "gp_[a-zA-Z0-9]{40}",
			Hidden:              true,
			System:              false,
		})

		return app.Save(collection)
	}, func(app core.App) error {
		collection, err := app.FindCollectionByNameOrId("users")
		if err != nil {
			return err
		}

		collection.Fields.RemoveById("text_api_token_001")

		return app.Save(collection)
	})
}
