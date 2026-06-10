package config

import (
	"flag"
	"fmt"
	"log"
	"path/filepath"
	"sync"

	"github.com/ilyakaznacheev/cleanenv"
)

type Config struct {
	ResendAPIKey string `env:"RESEND_API_KEY"`
	MailFrom     string `env:"MAIL_FROM" env-default:"GoPort <noreply@contact.goport.uz>"`
	AppName      string `env:"APP_NAME" env-default:"GoPort"`
	AppURL       string `env:"APP_URL" env-default:"https://goport.uz"`
}

var instance *Config
var once sync.Once

func GetConfig() *Config {
	once.Do(func() {
		log.Print("gather config")

		instance = &Config{}

		rootPath := flag.String("root_path", "", "Root path")
		flag.Parse()

		envFilePath, err := filepath.Abs(*rootPath + ".env")
		if err != nil {
			fmt.Println("Env file path error: ", err)
		}

		if err := cleanenv.ReadConfig(envFilePath, instance); err != nil {
			helpText := "Yurtal - Pocketbase template project!"
			help, _ := cleanenv.GetDescription(instance, &helpText)
			log.Print(help)
			fmt.Println("Application is starting with default config")
		}
	})
	return instance
}
