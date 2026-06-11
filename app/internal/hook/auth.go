package hook

import (
	"strings"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/model"
)

// registerAuthHooks wires account-lifecycle side effects:
//   - every new account gets a "default" CLI token automatically;
//   - OAuth sign-ins capture the provider avatar so the dashboard can show it.
func (h *Hook) registerAuthHooks(app *pocketbase.PocketBase) {
	// Give every freshly created account a default token so the dashboard has
	// something usable without the user clicking anything.
	app.OnRecordAfterCreateSuccess(model.UsersCollection).BindFunc(func(e *core.RecordEvent) error {
		if err := h.service.Tokens().EnsureDefault(e.Record.Id); err != nil {
			h.logger.Error("failed to create default token", "error", err, "userId", e.Record.Id)
		}
		return e.Next()
	})

	// Pull the avatar URL from the OAuth provider (e.g. Google) on sign-in.
	app.OnRecordAuthWithOAuth2Request(model.UsersCollection).BindFunc(func(e *core.RecordAuthWithOAuth2RequestEvent) error {
		if err := e.Next(); err != nil {
			return err
		}
		if e.Record == nil || e.OAuth2User == nil {
			return nil
		}
		avatar := strings.TrimSpace(e.OAuth2User.AvatarURL)
		if avatar == "" || e.Record.GetString("avatar_url") == avatar {
			return nil
		}
		e.Record.Set("avatar_url", avatar)
		if err := app.Save(e.Record); err != nil {
			h.logger.Error("failed to store oauth avatar", "error", err, "userId", e.Record.Id)
		}
		return nil
	})
}
