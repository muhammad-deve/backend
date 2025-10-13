package repository

import (
	"github.com/pocketbase/dbx"
	"gitlab.yurtal.tech/company/blitz/business-card/back/internal/repository/sqlite"
)

type AuthorizationI interface {
}

type I interface {
	Authorization() AuthorizationI
}

type repository struct {
	AuthorizationI
}

func (r *repository) Authorization() AuthorizationI {
	return r.AuthorizationI
}

func NewRepository(db dbx.Builder) I {
	return &repository{
		AuthorizationI: sqlite.NewAuthorization(db),
	}
}
