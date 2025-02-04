	package model

	import "time"

	type Book struct {
	    ID        string    `json:"id"`
	    Title     string    `json:"title"`
	    Rating    float64   `json:"rating"`
	    RateCount int       `json:"rateCount"`
	    Views     int       `json:"views"`
	    Created   time.Time `json:"created"`
	    Updated   time.Time `json:"updated"`
	}

	type BookRate struct {
	    ID      string    `json:"id"`
	    UserID  string    `json:"user"`
	    BookID  string    `json:"book"`
	    Rating  float64   `json:"rating"`
	    Created time.Time `json:"created"`
	    Updated time.Time `json:"updated"`
	}

