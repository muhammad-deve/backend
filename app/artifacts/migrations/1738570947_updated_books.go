package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

func init() {
	m.Register(func(app core.App) error {
		collection, err := app.FindCollectionByNameOrId("pbc_2170393721")
		if err != nil {
			return err
		}

		// add field
		if err := collection.Fields.AddMarshaledJSONAt(10, []byte(`{
			"cascadeDelete": false,
			"collectionId": "pbc_3582524359",
			"hidden": false,
			"id": "relation222307341",
			"maxSelect": 1,
			"minSelect": 0,
			"name": "suitAge",
			"presentable": false,
			"required": false,
			"system": false,
			"type": "relation"
		}`)); err != nil {
			return err
		}

		return app.Save(collection)
	}, func(app core.App) error {
		collection, err := app.FindCollectionByNameOrId("pbc_2170393721")
		if err != nil {
			return err
		}

		// remove field
		collection.Fields.RemoveById("relation222307341")

		return app.Save(collection)
	})
}
