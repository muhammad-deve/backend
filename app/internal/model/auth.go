package model

// SendOTPRequest is the payload for requesting an OTP code by email.
type SendOTPRequest struct {
	Email string `json:"email" form:"email"`
	Name  string `json:"name" form:"name"`
}

// SendOTPResponse is returned after an OTP has been issued and emailed.
type SendOTPResponse struct {
	OtpID   string `json:"otpId"`
	Message string `json:"message"`
}

// VerifyOTPRequest is the payload for verifying an OTP code.
type VerifyOTPRequest struct {
	OtpID string `json:"otpId" form:"otpId"`
	Code  string `json:"code" form:"code"`
}

// VerifyOTPResponse is returned after a successful OTP verification.
type VerifyOTPResponse struct {
	Email   string `json:"email"`
	Name    string `json:"name"`
	Valid   bool   `json:"valid"`
	Message string `json:"message"`
}

// CompleteRegistrationRequest finalizes signup by setting the account password
// after the OTP has been verified.
type CompleteRegistrationRequest struct {
	OtpID    string `json:"otpId" form:"otpId"`
	Code     string `json:"code" form:"code"`
	Password string `json:"password" form:"password"`
}

// CompleteRegistrationResponse is returned once the account is created.
type CompleteRegistrationResponse struct {
	UserID  string `json:"userId"`
	Email   string `json:"email"`
	Name    string `json:"name"`
	Message string `json:"message"`
}

// ForgotPasswordRequest requests a password-reset OTP for an existing account.
type ForgotPasswordRequest struct {
	Email string `json:"email" form:"email"`
}

// ResetPasswordRequest finalizes a password reset using a verified OTP.
type ResetPasswordRequest struct {
	OtpID    string `json:"otpId" form:"otpId"`
	Code     string `json:"code" form:"code"`
	Password string `json:"password" form:"password"`
}

// ResetPasswordResponse is returned once the password has been updated.
type ResetPasswordResponse struct {
	Email   string `json:"email"`
	Message string `json:"message"`
}

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
