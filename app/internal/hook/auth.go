package hook

import (
	"strings"
	"time"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/handler"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/model"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/service"
)

// registerAuthHooks wires account-lifecycle side effects:
//   - every new account gets a "default" CLI token automatically;
//   - password sign-ins are rate limited;
//   - OAuth sign-ins are checked against account pre-claims and capture the
//     provider avatar so the dashboard can show it.
func (h *Hook) registerAuthHooks(app *pocketbase.PocketBase) {
	// Give every freshly created account a default token so the dashboard has
	// something usable without the user clicking anything.
	app.OnRecordAfterCreateSuccess(model.UsersCollection).BindFunc(func(e *core.RecordEvent) error {
		if err := h.service.Tokens().EnsureDefault(e.Record.Id); err != nil {
			h.logger.Error("failed to create default token", "error", err, "userId", e.Record.Id)
		}
		return e.Next()
	})

	// Names go into GoPort emails. Check a name changed through the REST API
	// the same way as at sign-up, sized for the dashboard's two name fields.
	app.OnRecordUpdateRequest(model.UsersCollection).BindFunc(func(e *core.RecordRequestEvent) error {
		if e.HasSuperuserAuth() {
			return e.Next()
		}
		name := e.Record.GetString("name")
		if name == e.Record.Original().GetString("name") {
			return e.Next()
		}
		clean, err := model.CleanName(name, model.MaxProfileNameLength)
		if err != nil {
			return e.BadRequestError(err.Error(), nil)
		}
		e.Record.Set("name", clean)
		return e.Next()
	})

	// Delete sign-ups that never got past the email code, once a day.
	app.Cron().MustAdd("goportPurgeStaleSignups", "40 3 * * *", func() {
		deleted, err := h.service.Account().PurgeStalePending(7 * 24 * time.Hour)
		if err != nil {
			h.logger.Error("failed to purge stale sign-ups", "error", err, "deleted", deleted)
			return
		}
		if deleted > 0 {
			h.logger.Info("purged stale sign-ups", "deleted", deleted)
		}
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

	// Google sign-in. New accounts are built from the provider's data only,
	// and an unverified account can't be entered with a different email.
	app.OnRecordAuthWithOAuth2Request(model.UsersCollection).BindFunc(func(e *core.RecordAuthWithOAuth2RequestEvent) error {
		// createData would let the client choose the new account's email and
		// so pre-claim someone else's address. The site never sends it.
		e.CreateData = nil
		if e.OAuth2User != nil {
			// The provider name becomes the account name and goes into emails.
			e.OAuth2User.Name = model.SafeName(e.OAuth2User.Name)
		}

		providerEmail := ""
		if e.OAuth2User != nil {
			providerEmail = strings.TrimSpace(e.OAuth2User.Email)
		}
		switch oauthAccess(e.Record, providerEmail) {
		case oauthRefuseNoEmail:
			return e.BadRequestError("Your Google account has no verified email address.", nil)
		case oauthRefuseMismatch:
			h.logger.Warn("refused provider sign-in to an unverified account", "userId", e.Record.Id, "provider", e.ProviderName)
			return e.BadRequestError("This Google account can't open that GoPort account. Please sign in with your email and password.", nil)
		case oauthClaimPending:
			// The owner proved the address through the provider, and
			// PocketBase verifies the account next. Drop what anyone else
			// attached to it first.
			if err := h.service.Account().ReleasePending(e.Record, e.ProviderName, e.OAuth2User.Id); err != nil {
				h.logger.Error("failed to clear a pending account before provider sign-in", "error", err, "userId", e.Record.Id)
				return e.InternalServerError("Couldn't sign you in. Please try again.", nil)
			}
			// PocketBase verifies only on an exact email match.
			e.Record.SetEmail(providerEmail)
		}

		if err := e.Next(); err != nil {
			return err
		}
		if e.Record == nil || e.OAuth2User == nil {
			return nil
		}

		// Keep the provider avatar so the dashboard can show it.
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

type oauthDecision int

const (
	oauthAllow oauthDecision = iota
	oauthRefuseNoEmail
	oauthRefuseMismatch
	oauthClaimPending
)

// oauthAccess decides a provider sign-in. record is the account PocketBase
// matched (nil for a new one).
func oauthAccess(record *core.Record, providerEmail string) oauthDecision {
	if record == nil {
		if providerEmail == "" {
			return oauthRefuseNoEmail
		}
		return oauthAllow
	}
	if record.Verified() {
		return oauthAllow
	}
	if providerEmail == "" || !strings.EqualFold(record.Email(), providerEmail) {
		return oauthRefuseMismatch
	}
	return oauthClaimPending
}
