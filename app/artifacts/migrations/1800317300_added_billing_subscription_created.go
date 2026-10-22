package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

// billing_subscriptions had no creation time. Test-mode rows stored before a
// cutoff keep giving Pro, newer ones only to testers, so the billing service
// needs to know when a row was stored.
//
// Existing rows get the time of their earliest payment record (the initial
// invoice or order) when there is one. Rows without one stay empty and count
// as stored before the cutoff. Errors are logged, never returned, so startup
// can't fail.
func init() {
	m.Register(func(app core.App) error {
		addSubscriptionCreated(app)
		return nil
	}, func(app core.App) error {
		collection, err := app.FindCollectionByNameOrId("billing_subscriptions")
		if err != nil {
			return err
		}
		collection.Fields.RemoveByName("created")
		return app.Save(collection)
	})
}

func addSubscriptionCreated(app core.App) {
	logger := app.Logger()
	collection, err := app.FindCollectionByNameOrId("billing_subscriptions")
	if err != nil {
		logger.Error("subscription created field: collection not found", "error", err)
		return
	}
	if collection.Fields.GetByName("created") == nil {
		collection.Fields.Add(&core.AutodateField{Name: "created", OnCreate: true})
		if err := app.Save(collection); err != nil {
			logger.Error("subscription created field: add failed", "error", err)
			return
		}
	}

	_, err = app.DB().NewQuery(`
		UPDATE billing_subscriptions SET created = COALESCE((
			SELECT MIN(t.charged_at) FROM billing_transactions t
			WHERE COALESCE(t.charged_at, '') != ''
			AND (
				t.external_subscription_id = billing_subscriptions.external_id
				OR (t.external_type = 'order' AND billing_subscriptions.order_id != '' AND t.external_id = billing_subscriptions.order_id)
			)
		), '')
		WHERE COALESCE(created, '') = ''`).Execute()
	if err != nil {
		logger.Error("subscription created field: backfill failed", "error", err)
	}
}
