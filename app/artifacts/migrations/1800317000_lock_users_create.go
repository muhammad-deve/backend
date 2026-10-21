package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

// Closes public sign-up through PocketBase's REST API.
//
// The users collection kept PocketBase's default createRule "", so anyone
// could POST /api/collections/users/records with any email and log in without
// the email code. That skipped every signup check, including the block on
// throwaway addresses. Accounts are created by the app's own signup (which
// saves records server side and ignores this rule) and by Google sign-in,
// which PocketBase runs as an internal request in the "oauth2" context.
func init() {
	m.Register(func(app core.App) error {
		users, err := app.FindCollectionByNameOrId("users")
		if err != nil {
			return err
		}
		rule := `@request.context = "oauth2"`
		users.CreateRule = &rule
		return app.Save(users)
	}, func(app core.App) error {
		users, err := app.FindCollectionByNameOrId("users")
		if err != nil {
			return err
		}
		open := ""
		users.CreateRule = &open
		return app.Save(users)
	})
}
