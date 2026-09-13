package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

func init() {
	m.Register(func(app core.App) error {
		for _, collectionID := range []string{billingSubscriptionsCollectionID, billingTransactionsCollectionID} {
			collection, err := app.FindCollectionByNameOrId(collectionID)
			if err != nil {
				return err
			}
			collection.Fields.RemoveByName("card_brand")
			collection.Fields.RemoveByName("card_last_four")
			if err := app.Save(collection); err != nil {
				return err
			}
		}
		return nil
	}, func(app core.App) error {
		for _, collectionID := range []string{billingSubscriptionsCollectionID, billingTransactionsCollectionID} {
			collection, err := app.FindCollectionByNameOrId(collectionID)
			if err != nil {
				return err
			}
			collection.Fields.Add(
				&core.TextField{Name: "card_brand"},
				&core.TextField{Name: "card_last_four", Max: 4},
			)
			if err := app.Save(collection); err != nil {
				return err
			}
		}
		return nil
	})
}
