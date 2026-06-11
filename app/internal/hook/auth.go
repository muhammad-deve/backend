package hook

import (
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/security"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/model"
)

// registerAuthHooks ensures every user has a CLI token the moment their record
// is created, so `goport auth <token>` works immediately after signup.
func (h *Hook) registerAuthHooks(app *pocketbase.PocketBase) {
	app.OnRecordCreate(model.UsersCollection).BindFunc(func(e *core.RecordEvent) error {
		if e.Record.GetString("api_token") == "" {
			e.Record.Set("api_token", "gp_"+security.RandomString(40))
		}
		return e.Next()
	})
}
