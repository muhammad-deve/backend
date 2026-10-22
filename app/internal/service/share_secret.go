package service

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// publicShareSecrets can be read by anyone in this public repository: the old
// fallback key and the example value from .env.example and the docs. They
// never sign or check anything; they only decrypt token copies made with them.
var publicShareSecrets = []string{
	"goport-development-share-key-change-in-production",
	"replace-with-at-least-32-random-bytes",
}

func publicShareSecret(value string) bool {
	for _, public := range publicShareSecrets {
		if value == public {
			return true
		}
	}
	return false
}

// shareSecretFile holds the generated key, inside PocketBase's data directory.
const shareSecretFile = "share_signing.key"

// loadShareSecret returns the share signing key: SHARE_SIGNING_KEY when it is
// set to a private value, otherwise a random key generated once and kept in
// dataDir. persistent is false only when that file can't be read or written;
// the key then lasts until restart. It never fails, so it can't stop the
// server from starting.
func loadShareSecret(configured, dataDir string) (secret string, persistent bool) {
	switch {
	case publicShareSecret(configured):
		log.Print("WARNING: SHARE_SIGNING_KEY is a published example value and is ignored; set a private random value or leave it empty")
	case configured != "":
		if len(configured) < 32 {
			log.Print("WARNING: SHARE_SIGNING_KEY is shorter than 32 bytes; use a longer random value")
		}
		return configured, true
	}
	path := filepath.Join(dataDir, shareSecretFile)
	data, err := os.ReadFile(path)
	if err == nil {
		if value := strings.TrimSpace(string(data)); value != "" && !publicShareSecret(value) {
			if len(value) < 32 {
				log.Printf("WARNING: %s is shorter than 32 bytes", path)
			}
			return value, true
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		log.Printf("ERROR: couldn't read %s: %v; share unlocks and link copies made now won't survive a restart", path, err)
		return randomShareSecret(), false
	}
	secret = randomShareSecret()
	if err := writeShareSecret(path, secret); err != nil {
		log.Printf("ERROR: couldn't save %s: %v; share unlocks and link copies made now won't survive a restart", path, err)
		return secret, false
	}
	log.Printf("SHARE_SIGNING_KEY is empty; generated a share signing key in %s", path)
	return secret, true
}

func randomShareSecret() string {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		panic(err)
	}
	return hex.EncodeToString(value)
}

// writeShareSecret writes the key readable only by the server user, through a
// temporary file so a crash never leaves a partial key behind.
func writeShareSecret(path, secret string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".share_signing.key-*")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	if err := temporary.Chmod(0o600); err != nil && !errors.Is(err, errors.ErrUnsupported) {
		temporary.Close()
		return err
	}
	if _, err := temporary.WriteString(secret + "\n"); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporary.Name(), path)
}
