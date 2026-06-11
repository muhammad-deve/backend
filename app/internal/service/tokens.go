package service

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/model"
)

// ErrTokenNameTaken is returned when a user already has a token with the
// requested name.
var ErrTokenNameTaken = errors.New("you already have a token with this name")

// ErrTokenNotFound is returned when a token can't be found for the user.
var ErrTokenNotFound = errors.New("token not found")

type TokensI interface {
	List(userID string) ([]model.TokenItem, error)
	Create(userID, name string) (*model.TokenItem, error)
	Delete(userID, tokenID string) error
	// EnsureDefault creates the "default" token for a user if they have none.
	EnsureDefault(userID string) error
}

type tokensService struct {
	app *pocketbase.PocketBase
}

func NewTokensService(app *pocketbase.PocketBase) TokensI {
	return &tokensService{app: app}
}

func (s *tokensService) List(userID string) ([]model.TokenItem, error) {
	records, err := s.app.FindRecordsByFilter(
		model.TokensCollection,
		"user_id = {:user}",
		"created",
		200,
		0,
		dbx.Params{"user": userID},
	)
	if err != nil {
		return nil, err
	}

	items := make([]model.TokenItem, 0, len(records))
	for _, r := range records {
		items = append(items, model.TokenItem{
			ID:      r.Id,
			Name:    r.GetString("name"),
			Token:   r.GetString("token"),
			Created: r.GetString("created"),
		})
	}
	return items, nil
}

func (s *tokensService) Create(userID, name string) (*model.TokenItem, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("token name is required")
	}
	if len(name) > 50 {
		return nil, fmt.Errorf("token name must be 50 characters or fewer")
	}

	// Enforce per-user name uniqueness up front for a friendly error (the DB
	// unique index is the hard guarantee).
	if _, err := s.findByName(userID, name); err == nil {
		return nil, ErrTokenNameTaken
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	collection, err := s.app.FindCollectionByNameOrId(model.TokensCollection)
	if err != nil {
		return nil, err
	}

	record := core.NewRecord(collection)
	record.Set("user_id", userID)
	record.Set("name", name)
	record.Set("token", generateTokenValue())

	if err := s.app.Save(record); err != nil {
		if isUniqueViolation(err) {
			return nil, ErrTokenNameTaken
		}
		return nil, err
	}

	return &model.TokenItem{
		ID:      record.Id,
		Name:    record.GetString("name"),
		Token:   record.GetString("token"),
		Created: record.GetString("created"),
	}, nil
}

func (s *tokensService) Delete(userID, tokenID string) error {
	record, err := s.app.FindRecordById(model.TokensCollection, tokenID)
	if err != nil {
		return ErrTokenNotFound
	}
	if record.GetString("user_id") != userID {
		return ErrTokenNotFound
	}
	return s.app.Delete(record)
}

func (s *tokensService) EnsureDefault(userID string) error {
	records, err := s.app.FindRecordsByFilter(
		model.TokensCollection,
		"user_id = {:user}",
		"-created",
		1,
		0,
		dbx.Params{"user": userID},
	)
	if err != nil {
		return err
	}
	if len(records) > 0 {
		return nil
	}

	_, err = s.Create(userID, model.DefaultTokenName)
	if errors.Is(err, ErrTokenNameTaken) {
		return nil
	}
	return err
}

func (s *tokensService) findByName(userID, name string) (*core.Record, error) {
	return s.app.FindFirstRecordByFilter(
		model.TokensCollection,
		"user_id = {:user} && name = {:name}",
		dbx.Params{"user": userID, "name": name},
	)
}

// generateTokenValue mints a fresh, opaque token. It is prefixed so it's
// recognizable in the CLI, with a v4 UUID providing the randomness.
func generateTokenValue() string {
	return "gp_" + strings.ReplaceAll(uuid.NewString(), "-", "")
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique") || strings.Contains(msg, "constraint")
}
