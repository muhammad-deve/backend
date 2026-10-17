package migrations

import (
	"encoding/json"

	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

// Tunnels and tokens are written only by the server itself (the tunnel listener
// and the /api/tokens and /api/tunnels handlers), which bypass collection rules.
// The open createRule ("" = anyone, even signed out) and owner update rules let
// anyone write these rows straight through the PocketBase REST API: pick any
// subdomain with is_custom=false, then reuse it from the CLI on the Free plan,
// or mint a CLI token for a known user ID. Reads stay limited to the owner.
const (
	tunnelsCollectionID = "pbc_4233399332"
	tokensCollectionID  = "pbc_2638834880"
)

func init() {
	m.Register(func(app core.App) error {
		return setCollectionRules(app, map[string]string{
			tunnelsCollectionID: `{"createRule": null, "updateRule": null, "deleteRule": null}`,
			tokensCollectionID:  `{"createRule": null, "updateRule": null, "deleteRule": null}`,
		})
	}, func(app core.App) error {
		return setCollectionRules(app, map[string]string{
			tunnelsCollectionID: `{
				"createRule": "",
				"updateRule": "user = @request.auth.id",
				"deleteRule": "user = @request.auth.id"
			}`,
			tokensCollectionID: `{
				"createRule": "",
				"updateRule": "user_id = @request.auth.id",
				"deleteRule": "user_id = @request.auth.id"
			}`,
		})
	})
}

func setCollectionRules(app core.App, rules map[string]string) error {
	for id, data := range rules {
		collection, err := app.FindCollectionByNameOrId(id)
		if err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(data), &collection); err != nil {
			return err
		}
		if err := app.Save(collection); err != nil {
			return err
		}
	}
	return nil
}
