package model

// DefaultTokenName is the name of the token automatically created for every new
// account so users have a working token without creating one manually.
const DefaultTokenName = "default"

// TokenItem represents a single CLI token owned by a user.
type TokenItem struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Token   string `json:"token"`
	Created string `json:"created,omitempty"`
}

// CreateTokenRequest is the payload for creating a new named token.
type CreateTokenRequest struct {
	Name string `json:"name" form:"name"`
}

// VerifyTokenRequest is the payload the CLI sends to validate a token during
// `goport auth <token>`.
type VerifyTokenRequest struct {
	Token string `json:"token" form:"token"`
}

// TokenOwner describes the account a CLI token belongs to. It is returned when
// a token is successfully verified so the CLI can confirm which account the
// machine is now linked to.
type TokenOwner struct {
	UserID string `json:"userId"`
	Email  string `json:"email"`
	Name   string `json:"name"`
}
