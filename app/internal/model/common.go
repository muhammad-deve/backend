package model

import "github.com/pocketbase/pocketbase/core"

type defaultPagination struct {
	Page    int
	PerPage int
}

var DefaultPagination = defaultPagination{
	Page:    1,
	PerPage: 30,
}

type PaginatedItems struct {
	Items      []*core.Record `json:"items"`
	Page       int            `json:"page"`
	PerPage    int            `json:"perPage"`
	TotalPages int            `json:"totalPages"`
	TotalItems int            `json:"totalItems"`
}
