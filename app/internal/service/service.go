package service

import (
	"github.com/pocketbase/pocketbase"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/config"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/model"
)

type AuthorizationI interface {
	AmoCRMTokenExchange(req *model.AmoCRMTokenExchangeRequest) (*model.AmoCRMTokenExchangeResponse, error)
}

type I interface {
	Authorization() AuthorizationI
	TCP() TCPI
	OTP() OTPI
	Email() EmailI
}

type service struct {
	AuthorizationI
	tcpService   TCPI
	otpService   OTPI
	emailService EmailI
}

func (s *service) Authorization() AuthorizationI {
	return s.AuthorizationI
}

func (s *service) TCP() TCPI {
	return s.tcpService
}

func (s *service) OTP() OTPI {
	return s.otpService
}

func (s *service) Email() EmailI {
	return s.emailService
}

func NewService(app *pocketbase.PocketBase, cfg *config.Config) I {
	emailService := NewEmailService(cfg)
	return &service{
		AuthorizationI: NewAuthorizationS(app),
		tcpService:     NewTCPService(app),
		otpService:     NewOTPService(app, emailService),
		emailService:   emailService,
	}
}
