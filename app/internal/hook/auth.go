package hook

import (
	"strings"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/handler"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/model"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/service"
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

	// Limit wrong passwords per IP and per account. Only failures count, so
	// someone else's attempts can lock an account for at most the window.
	app.OnRecordAuthWithPasswordRequest(model.UsersCollection).BindFunc(func(e *core.RecordAuthWithPasswordRequestEvent) error {
		ip := handler.ClientIP(e.Request)
		if err := h.service.Throttle().LoginAllowed(ip, e.Identity); err != nil {
			seconds := 60
			if rl, ok := service.IsRateLimited(err); ok {
				seconds = rl.RetryAfterSeconds()
			}
			return handler.TooManyAttempts(e.RequestEvent, seconds)
		}
		if err := e.Next(); err != nil {
			h.service.Throttle().LoginFailed(ip, e.Identity)
			return err
		}
		h.service.Throttle().LoginSucceeded(e.Identity)
		return nil
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
