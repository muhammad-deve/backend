package model

type PasswordResetOTPConfirmRequest struct {
	OtpId    string `json:"otpId" form:"otpId"`
	Password string `json:"password" form:"password"`
}

type AmoCRMTokenExchangeRequest struct {
	Domain   string `json:"domain" form:"domain"`
	ClientID string `json:"client_id" form:"client_id"`
	Code     string `json:"code" form:"code"`
}

type AmoCRMTokenExchangeResponse struct {
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

type AmoCRMAccessTokenRequest struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	GrantType    string `json:"grant_type"`
	Code         string `json:"code"`
	RedirectURI  string `json:"redirect_uri"`
}
