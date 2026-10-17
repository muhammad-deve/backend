package app

import (
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/plugins/migratecmd"
	_ "gitlab.yurtal.tech/company/pocketbase-app-template/artifacts/migrations"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/config"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/handler"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/hook"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/service"
)

func NewApp(config *config.Config) *pocketbase.PocketBase {
	app := pocketbase.New()

	var services service.I

	app.OnServe().BindFunc(func(e *core.ServeEvent) error {
		logger := app.Logger()

		if err := configureGoogleOAuth(app, config); err != nil {
			logger.Error("failed to configure Google OAuth", "error", err)
		}

		services = service.NewService(app, config)

		// Start TCP tunnel listener on port 7000
		if err := services.TCP().Start(); err != nil {
			logger.Error("failed to start TCP listener", "error", err)
		}

		handlers := handler.NewHandler(logger, services, config)
		hooks := hook.New(logger, services)

		handlers.Register(e.Router)
		hooks.Register(app)

		return e.Next()
	})

	// On shutdown (every deploy) write out the tunnel traffic and connected
	// minutes still held in memory. This runs before PocketBase closes the DB.
	app.OnTerminate().BindFunc(func(e *core.TerminateEvent) error {
		if services != nil {
			services.TCP().Stop()
		}
		return e.Next()
	})

	migratecmd.MustRegister(app, app.RootCmd, migratecmd.Config{
		Automigrate: true,
	})

	return app
}
