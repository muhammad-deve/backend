package model

// RequestEmailChangeRequest starts verification of a new email address.
type RequestEmailChangeRequest struct {
	Email string `json:"email" form:"email"`
}

// ConfirmEmailChangeRequest applies the new email after OTP verification.
type ConfirmEmailChangeRequest struct {
	OtpID string `json:"otpId" form:"otpId"`
	Code  string `json:"code" form:"code"`
}

// EmailChangeResponse is returned when an email change is completed.
type EmailChangeResponse struct {
	Email   string `json:"email"`
	Token   string `json:"token"`
	Message string `json:"message"`
}

// ChangePasswordRequest verifies the existing password before replacing it.
type ChangePasswordRequest struct {
	CurrentPassword string `json:"currentPassword" form:"currentPassword"`
	NewPassword     string `json:"newPassword" form:"newPassword"`
	ConfirmPassword string `json:"confirmPassword" form:"confirmPassword"`
}

// ChangePasswordResponse confirms that the password has been updated.
type ChangePasswordResponse struct {
	Token   string `json:"token"`
	Message string `json:"message"`
}
