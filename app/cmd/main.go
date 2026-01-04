package main

import (
	"log"

	application "gitlab.yurtal.tech/company/pocketbase-app-template/internal/app"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/config"
)

func main() {
	log.Print("config initializing")
	cfg := config.GetConfig()

	app := application.NewApp(cfg)

	if err := app.Start(); err != nil {
		log.Fatal(err)
	}
}
