package apperror

import (
	"encoding/json"
	"net/http"
)

var (
	ErrNotFound = NewAppError(nil, "not found", "", http.StatusNotFound, false)
)

type AppError struct {
	Err              error  `json:"-"`
	Message          string `json:"message"`
	IsDevErr         bool   `json:"-"`
	DeveloperMessage string `json:"developer_message"`
	StatusCode       int    `json:"status_code"`
}

func (a *AppError) Error() string { return a.Message }

func (a *AppError) Unwrap() error { return a.Err }

func (a *AppError) Marshal() []byte {
	marshaled, err := json.Marshal(a)
	if err != nil {
		return nil
	}
	return marshaled
}

func NewAppError(err error, message, developerMessage string, code int, isDevErr bool) *AppError {
	return &AppError{
		Err:              err,
		Message:          message,
		DeveloperMessage: developerMessage,
		IsDevErr:         isDevErr,
		StatusCode:       code,
	}
}

func SystemError(err error) *AppError {
	return NewAppError(err, "internal system error", err.Error(), http.StatusInternalServerError, true)
}

func ClientError(err error, statusCode int) *AppError {
	return NewAppError(err, err.Error(), "", statusCode, false)
}
