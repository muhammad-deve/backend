package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

// Only superusers may delete accounts.
//
// The users collection kept PocketBase's default deleteRule
// "id = @request.auth.id", so anyone could delete their own account through
// the REST API. The billing rows cascaded away while Lemon Squeezy kept
// charging, and tunnels and tokens were left with an empty owner. Account
// deletion goes through support instead. A request hook refuses these deletes
// too, so a failure here is logged rather than stopping the server.
func init() {
	m.Register(func(app core.App) error {
		users, err := app.FindCollectionByNameOrId("users")
		if err != nil {
			app.Logger().Error("lock users delete: users collection not found", "error", err)
			return nil
		}
		if users.DeleteRule == nil {
			return nil
		}
		users.DeleteRule = nil
		if err := app.Save(users); err != nil {
			app.Logger().Error("lock users delete: save failed", "error", err)
		}
		return nil
	}, func(app core.App) error {
		users, err := app.FindCollectionByNameOrId("users")
		if err != nil {
			return err
		}
		rule := "id = @request.auth.id"
		users.DeleteRule = &rule
		return app.Save(users)
	})
}
