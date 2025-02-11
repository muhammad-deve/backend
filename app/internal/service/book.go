package service

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"gitlab.saidoff.uz/company/muslim-administration/reading/back/internal/model"
	"sort"
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

func (b *BookS) SuggestionMaker(genres []string) ([]model.Book, error) {
	var books []model.Book
	rows, err := b.db.Select("id", "name", "file", "bookImage", "author", "genres", "views", "rating",
		"rateCount", "collection", "totalpages", "suitAge", "info", "created", "updated").
		From("books").
		Rows()

	if err != nil {
		return nil, fmt.Errorf("failed to fetch books: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var book model.Book
		var rawGenres string // DB dagi genres ustuni string sifatida olinadi

		err := rows.Scan(&book.ID, &book.Name, &book.File, &book.BookImage, &book.Author, &rawGenres, &book.Views,
			&book.Rating, &book.RateCount, &book.Collection, &book.TotalPages, &book.SuitAge, &book.Info,
			&book.Created, &book.Updated)

		if err != nil {
			return nil, fmt.Errorf("scan error: %w", err)
		}

		if err := json.Unmarshal([]byte(rawGenres), &book.Genres); err != nil {
			return nil, fmt.Errorf("failed to parse genres: %w", err)
		}

		books = append(books, book)
	}

	sort.Slice(books, func(i, j int) bool {
		return books[i].Rating > books[j].Rating
	})

	return books, nil
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
