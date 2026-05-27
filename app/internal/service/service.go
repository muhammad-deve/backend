package service

import (
	"github.com/pocketbase/pocketbase"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/model"
)

type AuthorizationI interface {
	AmoCRMTokenExchange(req *model.AmoCRMTokenExchangeRequest) (*model.AmoCRMTokenExchangeResponse, error)
}

type I interface {
	Authorization() AuthorizationI
	TCP() TCPI
}

type service struct {
	AuthorizationI
	tcpService TCPI
}

func (s *service) Authorization() AuthorizationI {
	return s.AuthorizationI
}

func (s *service) TCP() TCPI {
	return s.tcpService
}

func NewService(app *pocketbase.PocketBase) I {
	return &service{
		AuthorizationI: NewAuthorizationS(app),
		tcpService:     NewTCPService(app),
	}
}
