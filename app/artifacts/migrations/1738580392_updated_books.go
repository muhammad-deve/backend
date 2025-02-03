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
			"convertURLs": false,
			"hidden": false,
			"id": "editor3414765911",
			"maxSize": 0,
			"name": "info",
			"presentable": false,
			"required": false,
			"system": false,
			"type": "editor"
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
		collection.Fields.RemoveById("editor3414765911")

		return app.Save(collection)
	})
}
