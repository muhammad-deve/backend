package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

const (
	billingSubscriptionsCollectionID = "pbc_3948530191"
	billingTransactionsCollectionID  = "pbc_3948530192"
)

func init() {
	m.Register(func(app core.App) error {
		users, err := app.FindCollectionByNameOrId("users")
		if err != nil {
			return err
		}

		subscriptions := core.NewBaseCollection("billing_subscriptions", billingSubscriptionsCollectionID)
		subscriptions.Fields.Add(
			&core.RelationField{Name: "user", CollectionId: users.Id, CascadeDelete: true, MinSelect: 1, MaxSelect: 1, Required: true},
			&core.TextField{Name: "external_id", Required: true},
			&core.TextField{Name: "customer_id"},
			&core.TextField{Name: "order_id"},
			&core.TextField{Name: "product_id"},
			&core.TextField{Name: "variant_id"},
			&core.TextField{Name: "plan_key"},
			&core.TextField{Name: "product_name"},
			&core.TextField{Name: "variant_name"},
			&core.TextField{Name: "status", Required: true},
			&core.TextField{Name: "card_brand"},
			&core.TextField{Name: "card_last_four", Max: 4},
			&core.DateField{Name: "renews_at"},
			&core.DateField{Name: "ends_at"},
			&core.DateField{Name: "trial_ends_at"},
			&core.DateField{Name: "provider_updated_at"},
			&core.BoolField{Name: "test_mode"},
		)
		subscriptions.Indexes = []string{
			"CREATE UNIQUE INDEX idx_billing_subscription_external ON billing_subscriptions (external_id)",
			"CREATE INDEX idx_billing_subscription_user_status ON billing_subscriptions (user, status)",
			"CREATE INDEX idx_billing_subscription_order ON billing_subscriptions (order_id)",
		}
		if err := app.Save(subscriptions); err != nil {
			return err
		}

		transactions := core.NewBaseCollection("billing_transactions", billingTransactionsCollectionID)
		transactions.Fields.Add(
			&core.RelationField{Name: "user", CollectionId: users.Id, CascadeDelete: true, MinSelect: 1, MaxSelect: 1, Required: true},
			&core.TextField{Name: "external_id", Required: true},
			&core.TextField{Name: "external_type", Required: true},
			&core.TextField{Name: "external_subscription_id"},
			&core.TextField{Name: "billing_reason"},
			&core.TextField{Name: "description"},
			&core.NumberField{Name: "amount_cents", OnlyInt: true},
			&core.NumberField{Name: "refunded_amount_cents", OnlyInt: true},
			&core.TextField{Name: "currency", Max: 3},
			&core.TextField{Name: "status", Required: true},
			&core.DateField{Name: "charged_at"},
			&core.TextField{Name: "card_brand"},
			&core.TextField{Name: "card_last_four", Max: 4},
			&core.TextField{Name: "invoice_url"},
			&core.DateField{Name: "provider_updated_at"},
			&core.BoolField{Name: "test_mode"},
		)
		transactions.Indexes = []string{
			"CREATE UNIQUE INDEX idx_billing_transaction_external ON billing_transactions (external_type, external_id)",
			"CREATE INDEX idx_billing_transaction_user_date ON billing_transactions (user, charged_at)",
			"CREATE INDEX idx_billing_transaction_subscription ON billing_transactions (external_subscription_id)",
		}
		return app.Save(transactions)
	}, func(app core.App) error {
		transactions, err := app.FindCollectionByNameOrId(billingTransactionsCollectionID)
		if err == nil {
			if err := app.Delete(transactions); err != nil {
				return err
			}
		}

		subscriptions, err := app.FindCollectionByNameOrId(billingSubscriptionsCollectionID)
		if err != nil {
			return err
		}
		return app.Delete(subscriptions)
	})
}
