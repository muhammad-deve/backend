package migrations

import (
	"encoding/json"

	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

// Enforce that a token value is globally unique and that token names are unique
// per user (so a user can't have two tokens with the same name).
func init() {
	m.Register(func(app core.App) error {
		collection, err := app.FindCollectionByNameOrId("pbc_2638834880")
		if err != nil {
			return err
		}

		if err := json.Unmarshal([]byte(`{
			"indexes": [
				"CREATE UNIQUE INDEX `+"`"+`idx_tokens_user_name`+"`"+` ON `+"`"+`tokens`+"`"+` (`+"`"+`user_id`+"`"+`, `+"`"+`name`+"`"+`)",
				"CREATE UNIQUE INDEX `+"`"+`idx_tokens_token`+"`"+` ON `+"`"+`tokens`+"`"+` (`+"`"+`token`+"`"+`)"
			]
		}`), &collection); err != nil {
			return err
		}

		return app.Save(collection)
	}, func(app core.App) error {
		collection, err := app.FindCollectionByNameOrId("pbc_2638834880")
		if err != nil {
			return err
		}

		if err := json.Unmarshal([]byte(`{
			"indexes": []
		}`), &collection); err != nil {
			return err
		}

		return app.Save(collection)
	})
}
