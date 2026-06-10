package app

import (
	"strings"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/auth"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/config"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/model"
)

func configureGoogleOAuth(app *pocketbase.PocketBase, cfg *config.Config) error {
	clientID := strings.TrimSpace(cfg.GoogleOAuthID)
	clientSecret := strings.TrimSpace(cfg.GoogleOAuthSecret)
	if clientID == "" || clientSecret == "" {
		app.Logger().Warn("skipping Google OAuth configuration; GOOGLE_OAUTH_ID and GOOGLE_OAUTH_SECRET must both be set")
		return nil
	}

	collection, err := app.FindCollectionByNameOrId(model.UsersCollection)
	if err != nil {
		return err
	}

	changed := false
	if !collection.OAuth2.Enabled {
		collection.OAuth2.Enabled = true
		changed = true
	}

	if collection.Fields.GetByName("name") != nil && collection.OAuth2.MappedFields.Name != "name" {
		collection.OAuth2.MappedFields.Name = "name"
		changed = true
	}

	provider := core.OAuth2ProviderConfig{
		Name:         auth.NameGoogle,
		ClientId:     clientID,
		ClientSecret: clientSecret,
		DisplayName:  "Google",
	}

	if upsertOAuthProvider(&collection.OAuth2.Providers, provider) {
		changed = true
	}

	if !changed {
		return nil
	}

	return app.Save(collection)
}

func upsertOAuthProvider(providers *[]core.OAuth2ProviderConfig, provider core.OAuth2ProviderConfig) bool {
	for i := range *providers {
		if (*providers)[i].Name != provider.Name {
			continue
		}

		if (*providers)[i].ClientId == provider.ClientId &&
			(*providers)[i].ClientSecret == provider.ClientSecret &&
			(*providers)[i].DisplayName == provider.DisplayName {
			return false
		}

		(*providers)[i].ClientId = provider.ClientId
		(*providers)[i].ClientSecret = provider.ClientSecret
		(*providers)[i].DisplayName = provider.DisplayName
		return true
	}

	*providers = append(*providers, provider)
	return true
}
