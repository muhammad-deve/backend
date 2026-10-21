package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

// Adds the Pro "direct link" mode: the link opens the file itself rather than
// the GoPort share page.
func init() {
	m.Register(func(app core.App) error {
		collection, err := app.FindCollectionByNameOrId(sharesCollectionID)
		if err != nil {
			return err
		}
		if collection.Fields.GetByName("direct") == nil {
			collection.Fields.Add(&core.BoolField{Name: "direct"})
		}
		return app.Save(collection)
	}, func(app core.App) error {
		collection, err := app.FindCollectionByNameOrId(sharesCollectionID)
		if err != nil {
			return err
		}
		collection.Fields.RemoveByName("direct")
		return app.Save(collection)
	})
}
