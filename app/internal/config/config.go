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
	ResendAPIKey                    string `env:"RESEND_API_KEY"`
	MailFrom                        string `env:"MAIL_FROM" env-default:"GoPort <noreply@contact.goport.uz>"`
	AppName                         string `env:"APP_NAME" env-default:"GoPort"`
	AppURL                          string `env:"APP_URL" env-default:"https://goport.uz"`
	SupportEmail                    string `env:"SUPPORT_EMAIL" env-default:"support@contact.goport.uz"`
	GoogleOAuthID                   string `env:"GOOGLE_OAUTH_ID"`
	GoogleOAuthSecret               string `env:"GOOGLE_OAUTH_SECRET"`
	LemonSqueezyKey                 string `env:"LEMON_SQUEEZY_KEY"`
	LemonSqueezyStoreID             string `env:"LEMON_SQUEEZY_STORE_ID"`
	LemonSqueezyProMonthlyVariantID string `env:"LEMON_SQUEEZY_PRO_MONTHLY_VARIANT_ID"`
	LemonSqueezyProYearlyVariantID  string `env:"LEMON_SQUEEZY_PRO_YEARLY_VARIANT_ID"`
	LemonSqueezyWebhookSecret       string `env:"LEMON_SQUEEZY_WEBHOOK_SECRET"`
	LemonSqueezyTestMode            bool   `env:"LEMON_SQUEEZY_TEST_MODE" env-default:"true"`
	// In test mode only these accounts (comma-separated emails) may upgrade.
	LemonSqueezyTestEmails         string `env:"LEMON_SQUEEZY_TEST_EMAILS"`
	LemonSqueezyAPIURL             string `env:"LEMON_SQUEEZY_API_URL" env-default:"https://api.lemonsqueezy.com/v1"`
	LemonSqueezyMonthlyAmountCents int64  `env:"LEMON_SQUEEZY_MONTHLY_AMOUNT_CENTS" env-default:"299"`
	LemonSqueezyYearlyAmountCents  int64  `env:"LEMON_SQUEEZY_YEARLY_AMOUNT_CENTS" env-default:"1999"`
	ShareStorageDir                string `env:"SHARE_STORAGE_DIR" env-default:"./pb_data/shares"`
	ShareSigningKey                string `env:"SHARE_SIGNING_KEY"`
	// Cloud file sharing. Free and Pro have separate limits; the total cap
	// protects the shared server disk however many accounts exist.
	ShareTotalStorageBytes         int64 `env:"SHARE_TOTAL_STORAGE_BYTES" env-default:"21474836480"`
	ShareFreeStorageBytes          int64 `env:"SHARE_FREE_STORAGE_BYTES" env-default:"104857600"`
	ShareFreeMaxUploadBytes        int64 `env:"SHARE_FREE_MAX_UPLOAD_BYTES" env-default:"26214400"`
	ShareFreeDefaultRetentionHours int   `env:"SHARE_FREE_DEFAULT_RETENTION_HOURS" env-default:"24"`
	ShareFreeMaxRetentionHours     int   `env:"SHARE_FREE_MAX_RETENTION_HOURS" env-default:"72"`
	ShareFreeMaxDownloads          int64 `env:"SHARE_FREE_MAX_DOWNLOADS" env-default:"100"`
	ShareFreeUploadersPerIP        int   `env:"SHARE_FREE_UPLOADERS_PER_IP" env-default:"5"`
	ShareProStorageBytes           int64 `env:"SHARE_PRO_STORAGE_BYTES" env-default:"5368709120"`
	ShareProMaxUploadBytes         int64 `env:"SHARE_PRO_MAX_UPLOAD_BYTES" env-default:"524288000"`
	ShareProDefaultRetentionHours  int   `env:"SHARE_PRO_DEFAULT_RETENTION_HOURS" env-default:"168"`
	ShareProMaxRetentionHours      int   `env:"SHARE_PRO_MAX_RETENTION_HOURS" env-default:"720"`
	// Emails with a code (sign-up, password reset, email change) per client
	// IP and for the whole server, per hour.
	AuthCodeSendsPerIP   int `env:"AUTH_CODE_SENDS_PER_IP" env-default:"10"`
	AuthCodeSendsPerHour int `env:"AUTH_CODE_SENDS_PER_HOUR" env-default:"300"`
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
		if instance.LemonSqueezyKey == "" || instance.LemonSqueezyStoreID == "" || instance.LemonSqueezyWebhookSecret == "" {
			log.Print("WARNING: Lemon Squeezy billing is not fully configured")
		}
		if instance.LemonSqueezyTestMode {
			log.Print("WARNING: ==== LEMON_SQUEEZY_TEST_MODE is ON. Checkouts use Lemon Squeezy test mode and only LEMON_SQUEEZY_TEST_EMAILS can upgrade to Pro. Set LEMON_SQUEEZY_TEST_MODE=false to take real payments. ====")
		}
		if len(instance.ShareSigningKey) < 32 {
			log.Print("WARNING: SHARE_SIGNING_KEY should contain at least 32 random bytes before enabling cloud sharing")
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
