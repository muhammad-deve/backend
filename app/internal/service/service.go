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
	Dashboard() DashboardI
	Tokens() TokensI
}

type service struct {
	AuthorizationI
	tcpService       TCPI
	otpService       OTPI
	emailService     EmailI
	dashboardService DashboardI
	tokensService    TokensI
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

func (s *service) Dashboard() DashboardI {
	return s.dashboardService
}

func (s *service) Tokens() TokensI {
	return s.tokensService
}

func NewService(app *pocketbase.PocketBase, cfg *config.Config) I {
	emailService := NewEmailService(cfg)
	tokensService := NewTokensService(app)
	return &service{
		AuthorizationI:   NewAuthorizationS(app),
		tcpService:       NewTCPService(app),
		otpService:       NewOTPService(app, emailService),
		emailService:     emailService,
		dashboardService: NewDashboardService(app, tokensService),
		tokensService:    tokensService,
	}
}
