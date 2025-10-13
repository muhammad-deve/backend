package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase"
	"gitlab.yurtal.tech/company/blitz/business-card/back/internal/model"
)

type AuthorizationS struct {
	app *pocketbase.PocketBase
}

func NewAuthorizationS(app *pocketbase.PocketBase) *AuthorizationS {
	return &AuthorizationS{app: app}
}

func (a *AuthorizationS) AmoCRMTokenExchange(req *model.AmoCRMTokenExchangeRequest) (*model.AmoCRMTokenExchangeResponse, error) {
	filter := fmt.Sprintf("domain = '%s' && clientId = '%s'", req.Domain, req.ClientID)
	amoCRMConfig, err := a.app.FindFirstRecordByFilter(model.AmoCredentialsCollection, filter)
	if err != nil {
		return nil, err
	}
	if amoCRMConfig == nil {
		return nil, fmt.Errorf("no amoCRM credentials found")
	}
	clientSecret := amoCRMConfig.GetString("clientSecret")
	redirectUri := amoCRMConfig.GetString("redirectUri")
	referer := req.Domain

	if clientSecret == "" || redirectUri == "" {
		fmt.Println("Server is missing AMOCRM_CLIENT_SECRET or AMOCRM_REDIRECT_URI")
		return nil, fmt.Errorf("server is missing AMOCRM_CLIENT_SECRET or AMOCRM_REDIRECT_URI")
	}

	// Normalize domain from referer (can be full URL or hostname)
	domain := strings.TrimSpace(referer)
	if strings.HasPrefix(domain, "http://") || strings.HasPrefix(domain, "https://") {
		u, err := url.Parse(domain)
		if err == nil && u.Host != "" {
			domain = u.Host
		}
	}
	domain = strings.TrimSuffix(domain, "/")
	if domain == "" {
		return nil, fmt.Errorf("invalid referer domain")
	}

	payload := &model.AmoCRMAccessTokenRequest{
		ClientID:     req.ClientID,
		ClientSecret: clientSecret,
		GrantType:    model.AmoCRMGrantTypeAuthorizationCode,
		Code:         req.Code,
		RedirectURI:  redirectUri,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		fmt.Println("Failed to marshal request")
		return nil, fmt.Errorf("failed to marshal request")
	}

	urlStr := fmt.Sprintf("https://%s/oauth2/access_token", domain)
	client := &http.Client{Timeout: 10 * time.Second}
	atReq, err := http.NewRequest("POST", urlStr, bytes.NewBuffer(body))
	if err != nil {
		fmt.Println("Failed to create request")
		return nil, fmt.Errorf("failed to create request")
	}
	atReq.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(atReq)
	if err != nil {
		fmt.Println("Request error:", err)
		return nil, fmt.Errorf("request error: %v", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		fmt.Println("Token exchange failed:", string(respBody))
		return nil, fmt.Errorf("token exchange failed: %s", string(respBody))
	}

	var tokenResp model.AmoCRMTokenExchangeResponse
	if err := json.Unmarshal(respBody, &tokenResp); err != nil {
		fmt.Println("Failed to parse token response")
		return nil, fmt.Errorf("failed to parse token response")
	}

	amoCRMConfig.Set("refreshToken", tokenResp.RefreshToken)
	if err := a.app.Save(amoCRMConfig); err != nil {
		return nil, err
	}

	return &tokenResp, nil
}
