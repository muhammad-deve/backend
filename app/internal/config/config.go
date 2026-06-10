package config

import (
	"flag"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/ilyakaznacheev/cleanenv"
)

type Config struct {
	ResendAPIKey      string `env:"RESEND_API_KEY"`
	MailFrom          string `env:"MAIL_FROM" env-default:"GoPort <noreply@contact.goport.uz>"`
	AppName           string `env:"APP_NAME" env-default:"GoPort"`
	AppURL            string `env:"APP_URL" env-default:"https://goport.uz"`
	GoogleOAuthID     string `env:"GOOGLE_OAUTH_ID"`
	GoogleOAuthSecret string `env:"GOOGLE_OAUTH_SECRET"`
}

var instance *Config
var once sync.Once

func GetConfig() *Config {
	once.Do(func() {
		log.Print("gather config")

		instance = &Config{}

		rootPath := flag.String("root_path", "", "Root path")
		flag.Parse()

		// Resolve the .env file. The app may be launched from different working
		// directories (e.g. the Makefile runs from app/cmd), so probe a few
		// candidate locations rather than assuming the cwd.
		if envFilePath, ok := findEnvFile(*rootPath); ok {
			if err := cleanenv.ReadConfig(envFilePath, instance); err != nil {
				log.Printf("failed to read env file %q: %v", envFilePath, err)
			} else {
				log.Printf("loaded config from %s", envFilePath)
			}
		} else {
			// No .env file found; fall back to OS environment + defaults.
			if err := cleanenv.ReadEnv(instance); err != nil {
				log.Printf("failed to read environment config: %v", err)
			}
			log.Print("no .env file found, using OS environment and defaults")
		}

		if instance.ResendAPIKey == "" {
			log.Print("WARNING: RESEND_API_KEY is empty; OTP emails will not be sent")
		}
	})
	return instance
}

// findEnvFile returns the first existing .env path among the candidate
// locations, accounting for the various working directories the binary may run
// from.
func findEnvFile(rootPath string) (string, bool) {
	candidates := []string{
		rootPath + ".env", // explicit root_path flag (if provided)
		".env",            // current working directory
		"../.env",         // app/cmd -> app/.env (Makefile case)
		"../../.env",
		"../../../.env",
		"app/.env",
	}

	for _, c := range candidates {
		abs, err := filepath.Abs(c)
		if err != nil {
			continue
		}
		if info, err := os.Stat(abs); err == nil && !info.IsDir() {
			return abs, true
		}
	}
	return "", false
}
