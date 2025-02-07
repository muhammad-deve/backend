package service

import (
	"errors"
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

func (a *AuthorizationS) BookRatingUpdate(e *core.RecordEvent) error {
	bookId := e.Record.GetString("book")
	userId := e.Record.GetString("user")
	newRating := e.Record.GetFloat("rating")

	var oldRating float64
	oldRatingQuery := fmt.Sprintf("SELECT rating FROM %s WHERE book = {:bookID} AND user = {:userID} LIMIT 1", model.UserBookRatesCollection)
	err := a.db.NewQuery(oldRatingQuery).
		Bind(dbx.Params{"bookID": bookId, "userID": userId}).
		Row(&oldRating)
	if err != nil {
		return err
	}

	var bookRating float64
	var rateCount int
	bookQuery := fmt.Sprintf("SELECT rating, rateCount FROM %s WHERE id = {:bookID} LIMIT 1", model.BooksCollection)
	err = a.db.NewQuery(bookQuery).
		Bind(dbx.Params{"bookID": bookId}).
		Row(&bookRating, &rateCount)
	if err != nil {
		return err
	}

	totalRating := bookRating*float64(rateCount) - oldRating + newRating
	bookRating = totalRating / float64(rateCount)

	updateQuery := fmt.Sprintf("UPDATE %s SET rating = {:rating} WHERE id = {:bookID}", model.BooksCollection)
	_, err = a.db.NewQuery(updateQuery).
		Bind(dbx.Params{"rating": bookRating, "bookID": bookId}).
		Execute()

	return err
}
func (a *AuthorizationS) BookRatingCalculate(e *core.RecordEvent) error {
	bookId := e.Record.GetString("book")
	userRating := e.Record.GetFloat("rating")

	var bookRating float64
	var rateCount int
	bookQuery := fmt.Sprintf("SELECT rating, rateCount FROM %s WHERE id = {:bookID} LIMIT 1", model.BooksCollection)
	err := a.db.NewQuery(bookQuery).
		Bind(dbx.Params{"bookID": bookId}).
		Row(&bookRating, &rateCount)
	if err != nil {
		return err
	}

	bookRating = (bookRating*float64(rateCount) + userRating) / float64(rateCount+1)
	rateCount++

	updateQuery := fmt.Sprintf("UPDATE %s SET rating = {:rating}, rateCount = {:rateCount} WHERE id = {:bookID}", model.BooksCollection)
	_, err = a.db.NewQuery(updateQuery).
		Bind(dbx.Params{"rating": bookRating, "rateCount": rateCount, "bookID": bookId}).
		Execute()

	return err
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

func (a *AuthorizationS) IncrementBookViews(bookID string) (int, error) {
	var newViews int

	queryStr := fmt.Sprintf(
		"UPDATE %s SET views = views + 1 WHERE id = {:bookId} RETURNING views", model.BooksCollection)

	err := a.db.NewQuery(queryStr).
		Bind(dbx.Params{"bookId": bookID}).
		Row(&newViews)

	if err != nil {
		return 0, errors.New("failed to update book views")
	}

	return newViews, nil
}
