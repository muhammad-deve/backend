package service

import (
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/model"
)

type AuthorizationI interface {
	OtpRequest(e *core.RecordCreateOTPRequestEvent) error
	ResetPasswordRequest(e *core.RecordRequestPasswordResetRequestEvent) error
	ResetPasswordOTPConfirm(req *model.PasswordResetOTPConfirmRequest) (string, error)
}

type I interface {
	Authorization() AuthorizationI
}

type service struct {
	AuthorizationI
}

func (s *service) Authorization() AuthorizationI {
	return s.AuthorizationI
}

func NewService(db dbx.Builder) I {
	return &service{
		AuthorizationI: NewAuthorizationS(db),
	}
}
