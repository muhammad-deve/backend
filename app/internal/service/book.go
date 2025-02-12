package service

import (
	"database/sql"
	"errors"
	"fmt"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"gitlab.saidoff.uz/company/muslim-administration/reading/back/internal/model"
	"math"
	"strconv"
	"strings"
)

type BookS struct {
	db dbx.Builder
}

func NewBookS(db dbx.Builder) *BookS {
	return &BookS{db: db}
}

func (b *BookS) BookRatingUpdate(e *core.RecordEvent) error {
	bookId := e.Record.GetString("book")
	userId := e.Record.GetString("user")
	newRating := e.Record.GetFloat("rating")

	var oldRating float64
	oldRatingQuery := fmt.Sprintf("SELECT rating FROM %s WHERE book = {:bookID} AND user = {:userID} LIMIT 1", model.UserBookRatesCollection)
	err := b.db.NewQuery(oldRatingQuery).
		Bind(dbx.Params{"bookID": bookId, "userID": userId}).
		Row(&oldRating)
	if err != nil {
		return err
	}

	var bookRating float64
	var rateCount int
	bookQuery := fmt.Sprintf("SELECT rating, rateCount FROM %s WHERE id = {:bookID} LIMIT 1", model.BooksCollection)
	err = b.db.NewQuery(bookQuery).
		Bind(dbx.Params{"bookID": bookId}).
		Row(&bookRating, &rateCount)
	if err != nil {
		return err
	}

	totalRating := bookRating*float64(rateCount) - oldRating + newRating
	bookRating = totalRating / float64(rateCount)

	updateQuery := fmt.Sprintf("UPDATE %s SET rating = {:rating} WHERE id = {:bookID}", model.BooksCollection)
	_, err = b.db.NewQuery(updateQuery).
		Bind(dbx.Params{"rating": bookRating, "bookID": bookId}).
		Execute()

	return err
}

func (b *BookS) BookRatingCalculate(e *core.RecordEvent) error {
	bookId := e.Record.GetString("book")
	userRating := e.Record.GetFloat("rating")

	var bookRating float64
	var rateCount int
	bookQuery := fmt.Sprintf("SELECT rating, rateCount FROM %s WHERE id = {:bookID} LIMIT 1", model.BooksCollection)
	err := b.db.NewQuery(bookQuery).
		Bind(dbx.Params{"bookID": bookId}).
		Row(&bookRating, &rateCount)
	if err != nil {
		return err
	}

	bookRating = (bookRating*float64(rateCount) + userRating) / float64(rateCount+1)
	rateCount++

	updateQuery := fmt.Sprintf("UPDATE %s SET rating = {:rating}, rateCount = {:rateCount} WHERE id = {:bookID}", model.BooksCollection)
	_, err = b.db.NewQuery(updateQuery).
		Bind(dbx.Params{"rating": bookRating, "rateCount": rateCount, "bookID": bookId}).
		Execute()

	return err
}

func (b *BookS) IncrementBookViews(id string) error {
	var newViews int

	queryStr := fmt.Sprintf(
		"UPDATE %s SET views = views + 1 WHERE id = {:bookId} RETURNING views", model.BooksCollection)

	err := b.db.NewQuery(queryStr).
		Bind(dbx.Params{"bookId": id}).
		Row(&newViews)

	if err != nil {
		_ = errors.New("failed to update book views")
	}

	return nil
}

func (b *BookS) SuggestionMaker(e *core.RequestEvent) (*model.PaginatedItems, error) {
	query := e.Request.URL.Query()

	page, err := strconv.Atoi(query.Get("page"))
	if err != nil || page <= 0 {
		page = model.DefaultPagination.Page
	}

	perPage, err := strconv.Atoi(query.Get("perPage"))
	if err != nil || perPage <= 0 {
		perPage = model.DefaultPagination.PerPage
	}

	limit := perPage
	offset := (page - 1) * perPage

	var genres []string
	if e.Auth != nil {
		genres = e.Auth.GetStringSlice("favourite_genre")
	}

	genresFilterStr := makeGenresFilterStr(genres)
	books, err := e.App.FindRecordsByFilter(
		model.BooksCollection,
		genresFilterStr,
		"-rating",
		limit,
		offset,
	)

	totalItems := len(books)
	totalPages := int(math.Ceil(float64(totalItems) / float64(perPage)))
	result := &model.PaginatedItems{
		Items:      books,
		Page:       page,
		PerPage:    perPage,
		TotalPages: totalPages,
		TotalItems: totalItems,
	}

	return result, err
}

func makeGenresFilterStr(genres []string) string {
	if len(genres) == 0 {
		return ""
	}

	var conditions []string
	for _, genre := range genres {
		conditions = append(conditions, fmt.Sprintf("genres?~'%s'", genre))
	}

	return strings.Join(conditions, " || ")
}

func (b *BookS) UserBookSaved(e *core.RecordRequestEvent) error {
	book := e.Record.GetString("id")

	if e.Auth != nil {
		userId := e.Auth.Id
		if userId != "" {
			r, err := e.App.FindFirstRecordByFilter(model.UserSavedBooksCollection, "book={:id} && user={:userId}", dbx.Params{"id": book, "userId": userId})
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}

			if err == nil {
				e.Record.Set("isSaved", true)
				savedId := r.GetString("id")
				e.Record.Set("savedId", savedId)
			}
		}
	}

	return nil
}
