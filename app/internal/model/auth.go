package model

type PasswordResetOTPConfirmRequest struct {
	OtpId    string `json:"otpId" form:"otpId"`
	Password string `json:"password" form:"password"`
}
