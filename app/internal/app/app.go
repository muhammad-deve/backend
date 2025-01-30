package app

import (
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/plugins/migratecmd"
	_ "gitlab.saidoff.uz/company/muslim-administration/reading/back/artifacts/migrations"
	"gitlab.saidoff.uz/company/muslim-administration/reading/back/internal/config"
	"gitlab.saidoff.uz/company/muslim-administration/reading/back/internal/handler"
	"gitlab.saidoff.uz/company/muslim-administration/reading/back/internal/hook"
	"gitlab.saidoff.uz/company/muslim-administration/reading/back/internal/service"
)

func NewApp(config *config.Config) *pocketbase.PocketBase {
	app := pocketbase.New()

	app.OnServe().BindFunc(func(e *core.ServeEvent) error {
		db := app.DB()
		logger := app.Logger()

		services := service.NewService(db)

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
