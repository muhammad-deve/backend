package service

import (
	"github.com/pocketbase/pocketbase"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/config"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/model"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/repository"
)

type AuthorizationI interface {
	AmoCRMTokenExchange(req *model.AmoCRMTokenExchangeRequest) (*model.AmoCRMTokenExchangeResponse, error)
}

type I interface {
	Authorization() AuthorizationI
	TCP() TCPI
	OTP() OTPI
	Account() AccountI
	Email() EmailI
	Dashboard() DashboardI
	Tokens() TokensI
}

type service struct {
	AuthorizationI
	tcpService       TCPI
	otpService       OTPI
	accountService   AccountI
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

func (s *service) Account() AccountI {
	return s.accountService
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
	repositories := repository.NewRepository(app)
	return &service{
		AuthorizationI:   NewAuthorizationS(app),
		tcpService:       NewTCPService(app, repositories.Tunnels()),
		otpService:       NewOTPService(app, emailService),
		accountService:   NewAccountService(app, emailService),
		emailService:     emailService,
		dashboardService: NewDashboardService(app, tokensService),
		tokensService:    tokensService,
	}
}
