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

	app.OnServe().BindFunc(func(e *core.ServeEvent) error {
		logger := app.Logger()

		services := service.NewService(app, config)

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

	migratecmd.MustRegister(app, app.RootCmd, migratecmd.Config{
		Automigrate: true,
	})

	return app
}
