package model

type Book struct {
	ID         string   `json:"id" db:"id"`
	Name       string   `json:"name" db:"name"`
	File       string   `json:"file" db:"file"`
	BookImage  string   `json:"bookImage" db:"book_image"`
	Author     string   `json:"author" db:"author"`
	Genres     []string `json:"genres" db:"genres"`
	Views      int      `json:"views" db:"views"`
	Rating     float64  `json:"rating" db:"rating"`
	IsSaved    bool     `json:"isSaved" db:"-"`
	RateCount  int      `json:"rateCount" db:"rate_count"`
	Collection string   `json:"collection" db:"collection"`
	TotalPages int      `json:"totalpages" db:"total_pages"`
	SuitAge    string   `json:"suitAge" db:"suit_age"`
	Info       string   `json:"info" db:"info"`
	SavedID    string   `json:"savedId" db:"-"`
	Created    string   `json:"created" db:"created"`
	Updated    string   `json:"updated" db:"updated"`
}
