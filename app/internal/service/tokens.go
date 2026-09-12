package service

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"

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

// ErrInvalidToken is returned when a token value doesn't match any account.
var ErrInvalidToken = errors.New("invalid token")

var ErrPlanLimitReached = errors.New("plan limit reached")

type TokensI interface {
	List(userID string) ([]model.TokenItem, error)
	Create(userID, name string) (*model.TokenItem, error)
	Delete(userID, tokenID string) error
	// EnsureDefault creates the "default" token for a user if they have none.
	EnsureDefault(userID string) error
	// Verify resolves a token value to the account that owns it. It returns
	// ErrInvalidToken when the token doesn't belong to any account.
	Verify(token string) (*model.TokenOwner, error)
}

type tokensService struct {
	app      *pocketbase.PocketBase
	billing  BillingI
	createMu sync.Mutex
}

func NewTokensService(app *pocketbase.PocketBase, billing BillingI) TokensI {
	return &tokensService{app: app, billing: billing}
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

	s.createMu.Lock()
	defer s.createMu.Unlock()

	limits, err := s.billing.Plan(userID)
	if err != nil {
		return nil, err
	}
	existingTokens, err := s.List(userID)
	if err != nil {
		return nil, err
	}
	if len(existingTokens) >= limits.MaxTokens {
		if !limits.IsPro {
			return nil, fmt.Errorf("%w: the Free plan includes %d device token; upgrade to Pro for up to %d", ErrProRequired, limits.MaxTokens, model.ProMaxTokens)
		}
		return nil, fmt.Errorf("%w: Pro supports up to %d device tokens", ErrPlanLimitReached, limits.MaxTokens)
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

// Verify resolves a token value to its owning account. It is used by the CLI's
// `goport auth <token>` flow to confirm a token is real before saving it.
func (s *tokensService) Verify(token string) (*model.TokenOwner, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, ErrInvalidToken
	}

	rec, err := s.app.FindFirstRecordByFilter(model.TokensCollection, "token = {:token}", dbx.Params{
		"token": token,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrInvalidToken
	}
	if err != nil {
		return nil, err
	}

	userID := rec.GetString("user_id")
	if userID == "" {
		return nil, ErrInvalidToken
	}
	limits, err := s.billing.Plan(userID)
	if err != nil {
		return nil, err
	}
	tokens, err := s.List(userID)
	if err != nil {
		return nil, err
	}
	allowed := false
	for index, item := range tokens {
		if item.ID == rec.Id {
			allowed = index < limits.MaxTokens
			break
		}
	}
	if !allowed {
		return nil, fmt.Errorf("%w: this device token is outside your plan allowance; manage tokens or upgrade at https://goport.uz/dashboard#billing", ErrProRequired)
	}

	owner := &model.TokenOwner{UserID: userID}
	if user, err := s.app.FindRecordById(model.UsersCollection, userID); err == nil && user != nil {
		owner.Email = user.Email()
		owner.Name = user.GetString("name")
	}
	return owner, nil
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
