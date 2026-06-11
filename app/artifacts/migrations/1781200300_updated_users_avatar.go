package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

// Adds avatar_url to users. It is populated automatically from the OAuth
// provider (e.g. Google) on sign-in. Email/password accounts leave it empty and
// the dashboard falls back to the GoPort mark.
func init() {
	m.Register(func(app core.App) error {
		collection, err := app.FindCollectionByNameOrId("users")
		if err != nil {
			return err
		}

		if collection.Fields.GetByName("avatar_url") != nil {
			return nil
		}

		collection.Fields.Add(&core.TextField{
			Id:          "text_avatar_001",
			Name:        "avatar_url",
			Max:         600,
			Presentable: false,
		})

		return app.Save(collection)
	}, func(app core.App) error {
		collection, err := app.FindCollectionByNameOrId("users")
		if err != nil {
			return err
		}

		collection.Fields.RemoveById("text_avatar_001")

		return app.Save(collection)
	})
}
