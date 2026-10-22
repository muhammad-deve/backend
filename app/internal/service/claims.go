package service

import (
	"fmt"

	"github.com/pocketbase/pocketbase/core"
)

// releasePendingAccount clears what anyone may have attached to an unverified
// account before its owner proved the email: linked sign-in providers (all but
// the one being used now, if any) and CLI tokens. A fresh default token is
// created. The caller refreshes the session key when it saves the account.
func releasePendingAccount(app core.App, tokens TokensI, user *core.Record, keepProvider, keepProviderID string) error {
	links, err := app.FindAllExternalAuthsByRecord(user)
	if err != nil {
		return fmt.Errorf("list linked sign-ins: %w", err)
	}
	for _, link := range links {
		if keepProvider != "" && link.Provider() == keepProvider && link.ProviderId() == keepProviderID {
			continue
		}
		if err := app.Delete(link); err != nil {
			return fmt.Errorf("remove linked sign-in: %w", err)
		}
	}
	if err := tokens.Reset(user.Id); err != nil {
		return fmt.Errorf("reset tokens: %w", err)
	}
	return nil
}
