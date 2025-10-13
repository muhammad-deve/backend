package handler

import (
	"fmt"
	"net/http"

	"github.com/pocketbase/pocketbase/core"
	"gitlab.yurtal.tech/company/blitz/business-card/back/internal/model"
)

func (h *Handler) AuthHandler(e *core.RequestEvent) error {
	q := e.Request.URL.Query()
	code := q.Get("code")
	referer := q.Get("referer")
	clientID := q.Get("client_id")

	if code == "" || referer == "" || clientID == "" {
		fmt.Println("Missing required query params: code, referer, client_id")
		return fmt.Errorf("missing required query params: code, referer, client_id")
	}

	req := model.AmoCRMTokenExchangeRequest{
		Domain:   referer,
		ClientID: clientID,
		Code:     code,
	}

	resp, err := h.service.Authorization().AmoCRMTokenExchange(&req)
	if err != nil {
		return err
	}

	return e.JSON(http.StatusOK, resp)
}
