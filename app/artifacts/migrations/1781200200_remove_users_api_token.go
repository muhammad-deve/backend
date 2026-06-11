package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

// The single per-user api_token is superseded by the dedicated `tokens`
// collection (multiple named tokens per account), so drop it.
func init() {
	m.Register(func(app core.App) error {
		collection, err := app.FindCollectionByNameOrId("users")
		if err != nil {
			return err
		}

		collection.Fields.RemoveById("text_api_token_001")

		return app.Save(collection)
	}, func(app core.App) error {
		collection, err := app.FindCollectionByNameOrId("users")
		if err != nil {
			return err
		}

		if collection.Fields.GetByName("api_token") == nil {
			collection.Fields.Add(&core.TextField{
				Id:                  "text_api_token_001",
				Name:                "api_token",
				Max:                 64,
				AutogeneratePattern: "gp_[a-zA-Z0-9]{40}",
				Hidden:              true,
			})
		}

		return app.Save(collection)
	})
}
