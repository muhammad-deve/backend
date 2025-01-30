package sqlite

import "github.com/pocketbase/dbx"

type AuthorizationPg struct {
	db dbx.Builder
}

func NewAuthorization(db dbx.Builder) *AuthorizationPg {
	return &AuthorizationPg{
		db: db,
	}
}
