package service

import (
	"fmt"
	"strings"

	"github.com/resend/resend-go/v2"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/config"
)

type EmailI interface {
	SendOTP(toEmail, name, code string) error
}

type emailService struct {
	client *resend.Client
	cfg    *config.Config
}

func NewEmailService(cfg *config.Config) EmailI {
	var client *resend.Client
	if cfg != nil && cfg.ResendAPIKey != "" {
		client = resend.NewClient(cfg.ResendAPIKey)
	}
	return &emailService{
		client: client,
		cfg:    cfg,
	}
}

func (s *emailService) SendOTP(toEmail, name, code string) error {
	if s.client == nil {
		return fmt.Errorf("email service is not configured: missing RESEND_API_KEY")
	}

	params := &resend.SendEmailRequest{
		From:    s.cfg.MailFrom,
		To:      []string{toEmail},
		Subject: fmt.Sprintf("%s verification code: %s", s.cfg.AppName, code),
		Html:    s.otpHTML(name, code),
		Text:    s.otpText(name, code),
	}

	if _, err := s.client.Emails.Send(params); err != nil {
		return fmt.Errorf("failed to send OTP email: %w", err)
	}
	return nil
}

// otpText is the plain-text fallback for clients that don't render HTML.
func (s *emailService) otpText(name, code string) string {
	greeting := "Hi"
	if name != "" {
		greeting = "Hi " + name
	}
	return fmt.Sprintf(
		"%s,\n\nYour %s verification code is: %s\n\nThis code expires in 5 minutes. "+
			"If you didn't request it, you can safely ignore this email.\n\n— The %s team",
		greeting, s.cfg.AppName, code, s.cfg.AppName,
	)
}

// otpHTML renders a dark, GoPort-branded OTP email matching the product look
// (near-black background, white wordmark, green accent ring/code).
func (s *emailService) otpHTML(name, code string) string {
	greeting := "Hi"
	if name != "" {
		greeting = "Hi " + escapeHTML(name)
	}

	appName := escapeHTML(s.cfg.AppName)
	appURL := s.cfg.AppURL

	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<meta name="color-scheme" content="dark">
<title>%[1]s verification code</title>
</head>
<body style="margin:0;padding:0;background-color:#070707;color:#ffffff;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;">
  <div style="display:none;max-height:0;overflow:hidden;opacity:0;">Your %[1]s verification code is %[2]s</div>
  <table role="presentation" width="100%%" cellpadding="0" cellspacing="0" style="background-color:#070707;padding:40px 16px;">
    <tr>
      <td align="center">
        <table role="presentation" width="100%%" cellpadding="0" cellspacing="0" style="max-width:480px;background-color:#121212;border:1px solid #1f1f1f;border-radius:16px;overflow:hidden;">
          <tr>
            <td style="padding:32px 32px 8px 32px;" align="center">
              <div style="font-size:26px;font-weight:700;letter-spacing:-0.5px;color:#ffffff;">
                Go<span style="color:#3ee68b;">Port</span>
              </div>
            </td>
          </tr>
          <tr>
            <td style="padding:16px 32px 0 32px;color:#ffffff;font-size:16px;line-height:1.5;">
              <p style="margin:0 0 8px 0;">%[3]s,</p>
              <p style="margin:0;color:#a3a3a3;">Use the verification code below to continue signing in to %[1]s.</p>
            </td>
          </tr>
          <tr>
            <td style="padding:28px 32px;" align="center">
              <div style="display:inline-block;background-color:#0c0c0c;border:1px solid #2a2a2a;border-radius:12px;padding:18px 28px;">
                <span style="font-family:'Courier New',Courier,monospace;font-size:36px;font-weight:700;letter-spacing:10px;color:#3ee68b;">%[2]s</span>
              </div>
            </td>
          </tr>
          <tr>
            <td style="padding:0 32px 8px 32px;color:#a3a3a3;font-size:14px;line-height:1.5;" align="center">
              This code expires in <strong style="color:#ffffff;">5 minutes</strong>.
            </td>
          </tr>
          <tr>
            <td style="padding:8px 32px 32px 32px;color:#6b6b6b;font-size:13px;line-height:1.5;" align="center">
              If you didn't request this code, you can safely ignore this email.
            </td>
          </tr>
          <tr>
            <td style="padding:20px 32px;background-color:#0c0c0c;border-top:1px solid #1f1f1f;color:#6b6b6b;font-size:12px;" align="center">
              <a href="%[4]s" style="color:#3ee68b;text-decoration:none;">%[5]s</a>
              <span style="color:#3a3a3a;"> &bull; </span> Secure tunnels for developers
            </td>
          </tr>
        </table>
      </td>
    </tr>
  </table>
</body>
</html>`, appName, escapeHTML(code), greeting, appURL, escapeHTML(stripScheme(appURL)))
}

// escapeHTML performs minimal HTML escaping for values interpolated into the
// email template (name, code, url text).
func escapeHTML(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&#39;",
	)
	return r.Replace(s)
}

// stripScheme removes the protocol prefix from a URL for display purposes.
func stripScheme(url string) string {
	url = strings.TrimPrefix(url, "https://")
	url = strings.TrimPrefix(url, "http://")
	return strings.TrimSuffix(url, "/")
}
