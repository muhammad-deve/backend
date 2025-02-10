package service

import (
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"gitlab.saidoff.uz/company/muslim-administration/reading/back/internal/model"
)

type AuthorizationI interface {
	OtpRequest(e *core.RecordCreateOTPRequestEvent) error
	ResetPasswordRequest(e *core.RecordRequestPasswordResetRequestEvent) error
	ResetPasswordOTPConfirm(req *model.PasswordResetOTPConfirmRequest) (string, error)
}
type BookI interface {
	IncrementBookViews(id string) error
	BookRatingCalculate(e *core.RecordEvent) error
	BookRatingUpdate(e *core.RecordEvent) error
	UserBookSaved(e *core.RecordRequestEvent) error
}

type I interface {
	Authorization() AuthorizationI
	Book() BookI
}

type service struct {
	AuthorizationI
	BookI
}

func (s *service) Authorization() AuthorizationI {
	return s.AuthorizationI
}
func (s *service) Book() BookI { return s.BookI }

func NewService(db dbx.Builder) I {
	return &service{
		AuthorizationI: NewAuthorizationS(db),
		BookI:          NewBookS(db),
	}
}
