package service

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/security"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/model"
)

var (
	ErrEmailTaken               = errors.New("this email is already in use")
	ErrCurrentPasswordIncorrect = errors.New("current password is incorrect")
)

type AccountI interface {
	RequestEmailChange(user *core.Record, req *model.RequestEmailChangeRequest) (*model.SendOTPResponse, error)
	ConfirmEmailChange(user *core.Record, req *model.ConfirmEmailChangeRequest) (*model.EmailChangeResponse, error)
	ChangePassword(user *core.Record, req *model.ChangePasswordRequest) (*model.ChangePasswordResponse, error)
}

type accountService struct {
	app     *pocketbase.PocketBase
	email   EmailI
	limiter *otpRateLimiter
}

func NewAccountService(app *pocketbase.PocketBase, email EmailI) AccountI {
	return &accountService{
		app:     app,
		email:   email,
		limiter: newOTPRateLimiter(time.Hour, 5, 60*time.Second),
	}
}

func (s *accountService) RequestEmailChange(user *core.Record, req *model.RequestEmailChangeRequest) (*model.SendOTPResponse, error) {
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" || !strings.Contains(email, "@") {
		return nil, fmt.Errorf("a valid email is required")
	}
	if strings.EqualFold(email, user.Email()) {
		return nil, fmt.Errorf("enter a different email address")
	}

	usersCollection, err := s.app.FindCollectionByNameOrId(model.UsersCollection)
	if err != nil {
		return nil, fmt.Errorf("users collection not found: %w", err)
	}
	existing, err := s.app.FindAuthRecordByEmail(usersCollection, email)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("failed to check email: %w", err)
	}
	if existing != nil && existing.Id != user.Id {
		return nil, ErrEmailTaken
	}

	if allowed, retryAfter := s.limiter.Allow("email-change:" + user.Id + ":" + email); !allowed {
		return nil, &errRateLimited{retryAfter: retryAfter}
	}

	code := security.RandomStringWithAlphabet(model.OTPCodeLength, model.OTPCodeAlphabet)
	otp := core.NewOTP(s.app)
	otp.SetCollectionRef(usersCollection.Id)
	otp.SetRecordRef(user.Id)
	otp.SetSentTo(email)
	otp.SetPassword(code)
	if err := s.app.Save(otp); err != nil {
		return nil, fmt.Errorf("failed to create OTP: %w", err)
	}

	if err := s.email.SendOTP(email, user.GetString("name"), code); err != nil {
		if delErr := s.app.Delete(otp); delErr != nil {
			s.app.Logger().Error("failed to delete email-change OTP", "error", delErr, "otpId", otp.Id)
		}
		return nil, err
	}

	return &model.SendOTPResponse{OtpID: otp.Id, Message: "Verification code sent"}, nil
}

func (s *accountService) ConfirmEmailChange(user *core.Record, req *model.ConfirmEmailChangeRequest) (*model.EmailChangeResponse, error) {
	otpID := strings.TrimSpace(req.OtpID)
	code := strings.TrimSpace(req.Code)
	if otpID == "" || code == "" {
		return nil, fmt.Errorf("otpId and code are required")
	}

	otp, err := s.app.FindOTPById(otpID)
	if err != nil || otp.RecordRef() != user.Id {
		return nil, fmt.Errorf("invalid or expired code")
	}
	if otp.HasExpired(model.OTPExpiry) {
		if delErr := s.app.Delete(otp); delErr != nil {
			s.app.Logger().Error("failed to delete expired email-change OTP", "error", delErr, "otpId", otp.Id)
		}
		return nil, fmt.Errorf("invalid or expired code")
	}
	if !otp.ValidatePassword(code) {
		return nil, fmt.Errorf("invalid or expired code")
	}

	newEmail := strings.ToLower(strings.TrimSpace(otp.SentTo()))
	usersCollection, err := s.app.FindCollectionByNameOrId(model.UsersCollection)
	if err != nil || otp.CollectionRef() != usersCollection.Id {
		return nil, fmt.Errorf("invalid or expired code")
	}
	existing, lookupErr := s.app.FindAuthRecordByEmail(usersCollection, newEmail)
	if lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
		return nil, fmt.Errorf("failed to check email: %w", lookupErr)
	}
	if existing != nil && existing.Id != user.Id {
		return nil, ErrEmailTaken
	}

	user.SetEmail(newEmail)
	user.SetVerified(true)
	if err := s.app.Save(user); err != nil {
		return nil, fmt.Errorf("failed to change email: %w", err)
	}
	if delErr := s.app.Delete(otp); delErr != nil {
		s.app.Logger().Error("failed to delete used email-change OTP", "error", delErr, "otpId", otp.Id)
	}
	token, err := user.NewAuthToken()
	if err != nil {
		return nil, fmt.Errorf("email changed but a new session couldn't be created: %w", err)
	}

	return &model.EmailChangeResponse{Email: newEmail, Token: token, Message: "Email changed"}, nil
}

func (s *accountService) ChangePassword(user *core.Record, req *model.ChangePasswordRequest) (*model.ChangePasswordResponse, error) {
	if !user.ValidatePassword(req.CurrentPassword) {
		return nil, ErrCurrentPasswordIncorrect
	}
	if req.NewPassword != req.ConfirmPassword {
		return nil, fmt.Errorf("new passwords do not match")
	}
	if ok, reason := model.ValidatePassword(req.NewPassword); !ok {
		return nil, fmt.Errorf("%s", reason)
	}
	if user.ValidatePassword(req.NewPassword) {
		return nil, fmt.Errorf("new password must be different from the current password")
	}

	user.SetPassword(req.NewPassword)
	if err := s.app.Save(user); err != nil {
		return nil, fmt.Errorf("failed to change password: %w", err)
	}
	token, err := user.NewAuthToken()
	if err != nil {
		return nil, fmt.Errorf("password changed but a new session couldn't be created: %w", err)
	}

	return &model.ChangePasswordResponse{Token: token, Message: "Password changed"}, nil
}
