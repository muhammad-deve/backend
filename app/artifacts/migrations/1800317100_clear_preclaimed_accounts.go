package migrations

import (
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

// Google sign-in accepted a client-supplied createData, so anyone could create
// an unverified account under someone else's email, linked to their own Google
// account, and later get in once the real owner finished sign-up. Normal
// pending email sign-ups have no linked providers, so an unverified account
// with one is such a pre-claim. This removes the provider links and CLI tokens
// of those accounts and ends their sessions. The accounts stay.
//
// Errors are logged and skipped so a bad row can't stop the server starting.
// Running it again finds nothing to do.
func init() {
	m.Register(func(app core.App) error {
		clearPreclaimedAccounts(app)
		return nil
	}, func(app core.App) error {
		return nil
	})
}

func clearPreclaimedAccounts(app core.App) {
	logger := app.Logger()
	users, err := app.FindCollectionByNameOrId("users")
	if err != nil {
		logger.Error("pre-claim cleanup: users collection not found", "error", err)
		return
	}

	var ids []string
	err = app.DB().NewQuery(`
		SELECT u.id FROM users u
		WHERE u.verified = FALSE
		AND EXISTS (
			SELECT 1 FROM _externalAuths ea
			WHERE ea.recordRef = u.id AND ea.collectionRef = {:collection}
		)`).Bind(dbx.Params{"collection": users.Id}).Column(&ids)
	if err != nil {
		logger.Error("pre-claim cleanup: query failed", "error", err)
		return
	}

	_, tokensErr := app.FindCollectionByNameOrId("tokens")
	for _, id := range ids {
		user, err := app.FindRecordById(users, id)
		if err != nil {
			logger.Error("pre-claim cleanup: load user", "error", err, "userId", id)
			continue
		}
		links, err := app.FindAllExternalAuthsByRecord(user)
		if err != nil {
			logger.Error("pre-claim cleanup: list links", "error", err, "userId", id)
			continue
		}
		for _, link := range links {
			if err := app.Delete(link); err != nil {
				logger.Error("pre-claim cleanup: remove link", "error", err, "userId", id)
			}
		}
		if tokensErr == nil {
			tokens, err := app.FindAllRecords("tokens", dbx.HashExp{"user_id": id})
			if err != nil {
				logger.Error("pre-claim cleanup: list tokens", "error", err, "userId", id)
			}
			for _, token := range tokens {
				if err := app.Delete(token); err != nil {
					logger.Error("pre-claim cleanup: remove token", "error", err, "userId", id)
				}
			}
		}
		user.RefreshTokenKey()
		if err := app.SaveNoValidate(user); err != nil {
			logger.Error("pre-claim cleanup: end sessions", "error", err, "userId", id)
		}
	}
	if len(ids) > 0 {
		logger.Warn("pre-claim cleanup: cleared unverified accounts with linked sign-ins", "count", len(ids))
	}
}
