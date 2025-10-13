package service

import (
	"github.com/pocketbase/pocketbase"
	"gitlab.yurtal.tech/company/blitz/business-card/back/internal/model"
)

type AuthorizationI interface {
	AmoCRMTokenExchange(req *model.AmoCRMTokenExchangeRequest) (*model.AmoCRMTokenExchangeResponse, error)
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

func NewService(app *pocketbase.PocketBase) I {
	return &service{
		AuthorizationI: NewAuthorizationS(app),
	}
}
