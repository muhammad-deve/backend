package migrations

import (
	"encoding/json"

	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

// Record when each CLI token was last used to open a tunnel. Without it the
// dashboard can only say "not tracked yet", which is exactly the information
// someone needs in order to tell which tokens are safe to revoke.
func init() {
	m.Register(func(app core.App) error {
		collection, err := app.FindCollectionByNameOrId("pbc_2638834880")
		if err != nil {
			return err
		}

		field := &core.DateField{}
		if err := json.Unmarshal([]byte(`{
			"hidden": false,
			"id": "date_tokens_last_used",
			"name": "last_used",
			"presentable": false,
			"required": false,
			"system": false,
			"type": "date"
		}`), field); err != nil {
			return err
		}
		collection.Fields.Add(field)

		return app.Save(collection)
	}, func(app core.App) error {
		collection, err := app.FindCollectionByNameOrId("pbc_2638834880")
		if err != nil {
			return err
		}
		collection.Fields.RemoveById("date_tokens_last_used")
		return app.Save(collection)
	})
}
