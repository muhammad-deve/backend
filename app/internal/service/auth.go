package service

import (
	_ "errors"
	"fmt"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"gitlab.saidoff.uz/company/muslim-administration/reading/back/internal/model"
	"net/http"
)

type AuthorizationS struct {
	db dbx.Builder
}

func (a *AuthorizationS) OtpRequest(e *core.RecordCreateOTPRequestEvent) error {
	//TODO implement me
	panic("implement me")
}

func NewAuthorizationS(db dbx.Builder) *AuthorizationS {
	return &AuthorizationS{db: db}
}

func (a *AuthorizationS) ResetPasswordRequest(e *core.RecordRequestPasswordResetRequestEvent) error {
	collectionRef := e.Collection.Id
	recordRef := e.Record.Id
	token, err := e.Record.NewPasswordResetToken()
	otpId := ""

	queryStr := fmt.Sprintf("INSERT INTO %s (collectionRef, recordRef, password, resetPasswordToken) VALUES ({:collectionRef}, {:recordRef}, {:password}, {:resetPasswordToken}) RETURNING id", model.OtpCollection)
	insertQ := a.db.NewQuery(queryStr).
		Bind(dbx.Params{
			"collectionRef":      collectionRef,
			"recordRef":          recordRef,
			"password":           model.StaticOtpCode,
			"resetPasswordToken": token,
		})

	err = insertQ.Row(&otpId)
	if err != nil {
		return err
	}

	err = e.JSON(http.StatusOK, map[string]string{"otpId": otpId})
	return err
}

func (a *AuthorizationS) ResetPasswordOTPConfirm(req *model.PasswordResetOTPConfirmRequest) (string, error) {
	token := ""
	queryStr := fmt.Sprintf("SELECT resetPasswordToken FROM %s WHERE id = {:otpId} AND password = {:password}", model.OtpCollection)
	err := a.db.NewQuery(queryStr).Bind(dbx.Params{"otpId": req.OtpId, "password": req.Password}).Row(&token)
	return token, err
}
