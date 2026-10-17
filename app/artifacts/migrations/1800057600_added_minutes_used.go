package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

// minutes_used counts how long tunnels stay connected. On tunnels it is per
// subdomain; on users it is the sum over all of the account's tunnels, so 10
// tunnels open for an hour add 600 minutes. The users copy is hidden, which in
// PocketBase also stops non-superusers from writing it through the REST API.
const (
	minutesUsedTunnelsFieldID = "number3311000001"
	minutesUsedUsersFieldID   = "number3311000002"
)

func init() {
	m.Register(func(app core.App) error {
		tunnels, err := app.FindCollectionByNameOrId(tunnelsCollectionID)
		if err != nil {
			return err
		}
		if err := tunnels.Fields.AddMarshaledJSON([]byte(`{
			"hidden": false,
			"id": "` + minutesUsedTunnelsFieldID + `",
			"max": null,
			"min": 0,
			"name": "minutes_used",
			"onlyInt": false,
			"presentable": false,
			"required": false,
			"system": false,
			"type": "number"
		}`)); err != nil {
			return err
		}
		if err := app.Save(tunnels); err != nil {
			return err
		}

		users, err := app.FindCollectionByNameOrId("users")
		if err != nil {
			return err
		}
		if err := users.Fields.AddMarshaledJSON([]byte(`{
			"hidden": true,
			"id": "` + minutesUsedUsersFieldID + `",
			"max": null,
			"min": 0,
			"name": "minutes_used",
			"onlyInt": false,
			"presentable": false,
			"required": false,
			"system": false,
			"type": "number"
		}`)); err != nil {
			return err
		}
		return app.Save(users)
	}, func(app core.App) error {
		tunnels, err := app.FindCollectionByNameOrId(tunnelsCollectionID)
		if err != nil {
			return err
		}
		tunnels.Fields.RemoveById(minutesUsedTunnelsFieldID)
		if err := app.Save(tunnels); err != nil {
			return err
		}

		users, err := app.FindCollectionByNameOrId("users")
		if err != nil {
			return err
		}
		users.Fields.RemoveById(minutesUsedUsersFieldID)
		return app.Save(users)
	})
}
