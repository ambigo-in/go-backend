package mailer

import (
	"fmt"

	"ambigo-backend/config"
	"ambigo-backend/internal/logger"

	"github.com/resend/resend-go/v3"
)

type ResendMailer struct {
	client       *resend.Client
	fromEmail    string
	fromName     string
	toTest       string
	useVerified  bool
}

func NewResendMailer(cfg *config.AppConfig) *ResendMailer {
	if cfg.ResendAPIKey == "" {
		logger.Log.Warn().Msg("RESEND_API_KEY empty — receptionist invite emails will be logged only")
		return &ResendMailer{
			fromEmail:   cfg.ResendFromEmail,
			fromName:    cfg.ResendFromName,
			toTest:      cfg.ResendToTest,
			useVerified: cfg.ResendUseVerified,
		}
	}
	return &ResendMailer{
		client:      resend.NewClient(cfg.ResendAPIKey),
		fromEmail:   cfg.ResendFromEmail,
		fromName:    cfg.ResendFromName,
		toTest:      cfg.ResendToTest,
		useVerified: cfg.ResendUseVerified,
	}
}

// SendReceptionistInvite sends temp credentials. In test mode (useVerified=false) it sends to RESEND_TO_TEST
// and logs the real recipient to avoid Domain not verified errors while ambigo.in is verifying.
func (m *ResendMailer) SendReceptionistInvite(realEmail, username, tempPassword, hospitalName string, roleName ...string) error {
	role := "staff"
	if len(roleName) > 0 && roleName[0] != "" {
		role = roleName[0]
	}
	if m.client == nil {
		logger.Log.Info().Str("to", realEmail).Str("username", username).Msg("[RESEND TEST MODE] Would send invite email (no API key)")
		return nil
	}
	to := realEmail
	if !m.useVerified {
		to = m.toTest
		if to == "" {
			to = "delivered@resend.dev"
		}
		logger.Log.Info().Str("real_to", realEmail).Str("test_to", to).Str("username", username).Msg("Resend test mode: redirecting email to test inbox")
	}
	from := fmt.Sprintf("%s <%s>", m.fromName, m.fromEmail)
	subject := fmt.Sprintf("Your %s %s account credentials", hospitalName, role)
	portalURL := "https://hospital-ambigo.netlify.app"
	html := fmt.Sprintf(`
		<div style="font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, Helvetica, Arial, sans-serif; max-width: 600px; margin: 0 auto; padding: 24px; border: 1px solid #FFE4CC; border-radius: 12px; background: #ffffff;">
			<div style="margin-bottom: 20px;">
				<span style="color: #FF520D; font-size: 24px; font-weight: 800;">Ambigo</span>
				<span style="color: #333333; font-size: 24px; font-weight: 300;"> Hospital</span>
			</div>
			<h2 style="color: #111827; margin-top: 0;">Welcome to Ambigo</h2>
			<p style="color: #4B5563; font-size: 15px; line-height: 1.5;">Your <b>%s</b> account has been created for <b>%s</b>.</p>
			<table style="background: #F9FAFB; padding: 16px; border-radius: 8px; width: 100%%; border: 1px solid #E5E7EB; margin: 20px 0;">
				<tr><td style="padding: 6px 0; color: #6B7280; font-size: 14px;"><b>Role</b></td><td style="padding: 6px 0; color: #111827; font-weight: 600;">%s</td></tr>
				<tr><td style="padding: 6px 0; color: #6B7280; font-size: 14px;"><b>Username</b></td><td style="padding: 6px 0; color: #111827; font-weight: 600;">%s</td></tr>
				<tr><td style="padding: 6px 0; color: #6B7280; font-size: 14px;"><b>Temporary Password</b></td><td style="padding: 6px 0;"><code style="background: #FFF; padding: 4px 8px; border-radius: 4px; border: 1px solid #D1D5DB; font-weight: bold; color: #FF520D;">%s</code></td></tr>
				<tr><td style="padding: 6px 0; color: #6B7280; font-size: 14px;"><b>Portal URL</b></td><td style="padding: 6px 0;"><a href="%s" target="_blank" style="color: #FF520D; word-break: break-all; font-weight: 600;">%s</a></td></tr>
			</table>
			<div style="margin: 25px 0; text-align: center;">
				<a href="%s" target="_blank" style="display: inline-block; background: #FF520D; color: #ffffff; padding: 12px 28px; text-decoration: none; border-radius: 8px; font-weight: 700; font-size: 15px;">Log in to Hospital Portal</a>
			</div>
			<p style="color: #6B7280; font-size: 13px; line-height: 1.4;">You will be asked to change your temporary password upon your first login.</p>
			<hr style="border: none; border-top: 1px solid #F3F4F6; margin: 20px 0;" />
			<p style="color: #9CA3AF; font-size: 11px;">If you have any questions, please contact your Hospital MD Administrator.</p>
		</div>
	`, role, hospitalName, role, username, tempPassword, portalURL, portalURL, portalURL)

	params := &resend.SendEmailRequest{
		From:    from,
		To:      []string{to},
		Subject: subject,
		Html:    html,
	}
	sent, err := m.client.Emails.Send(params)
	if err != nil {
		logger.Log.Error().Err(err).Str("to", to).Str("real_to", realEmail).Msg("Resend send failed")
		return err
	}
	logger.Log.Info().Str("id", sent.Id).Str("to", to).Str("real_to", realEmail).Msg("Resend invite sent")
	return nil
}

func (m *ResendMailer) SendHospitalApproveEmail(toEmail, hospitalName, city string) error {
	if m.client == nil {
		logger.Log.Info().Str("to", toEmail).Str("hospital", hospitalName).Msg("[RESEND TEST MODE] Would send hospital approve email (no API key)")
		return nil
	}
	to := toEmail
	if !m.useVerified {
		if m.toTest != "" {
			to = m.toTest
		} else {
			to = "delivered@resend.dev"
		}
		logger.Log.Info().Str("real_to", toEmail).Str("test_to", to).Msg("Resend test mode: redirecting hospital approve email")
	}
	from := fmt.Sprintf("%s <%s>", m.fromName, m.fromEmail)
	subject := fmt.Sprintf("Your hospital %s has been approved", hospitalName)
	html := fmt.Sprintf(`
		<div style="font-family: sans-serif; max-width: 600px; margin: 0 auto;">
			<h2 style="color: #FF520D;">Hospital Approved</h2>
			<p>Congratulations! Your hospital <b>%s</b> in <b>%s</b> has been approved on Ambigo.</p>
			<p>You can now log in via the <b>Hospital MD Login</b> section in the Ambigo app using your registered mobile number and password.</p>
			<p style="color: #666; font-size: 12px;">If you have not set a password yet, log in with OTP and set it.</p>
		</div>
	`, hospitalName, city)
	params := &resend.SendEmailRequest{From: from, To: []string{to}, Subject: subject, Html: html}
	sent, err := m.client.Emails.Send(params)
	if err != nil {
		logger.Log.Error().Err(err).Str("to", to).Str("real_to", toEmail).Msg("Resend approve email failed")
		return err
	}
	logger.Log.Info().Str("id", sent.Id).Str("to", to).Str("real_to", toEmail).Msg("Resend hospital approve sent")
	return nil
}

func (m *ResendMailer) SendHospitalRejectEmail(toEmail, hospitalName, reason string) error {
	if m.client == nil {
		logger.Log.Info().Str("to", toEmail).Str("hospital", hospitalName).Msg("[RESEND TEST MODE] Would send hospital reject email (no API key)")
		return nil
	}
	to := toEmail
	if !m.useVerified {
		if m.toTest != "" {
			to = m.toTest
		} else {
			to = "delivered@resend.dev"
		}
		logger.Log.Info().Str("real_to", toEmail).Str("test_to", to).Msg("Resend test mode: redirecting hospital reject email")
	}
	from := fmt.Sprintf("%s <%s>", m.fromName, m.fromEmail)
	subject := fmt.Sprintf("Your hospital %s application was rejected", hospitalName)
	html := fmt.Sprintf(`
		<div style="font-family: sans-serif; max-width: 600px; margin: 0 auto;">
			<h2 style="color: #DC2626;">Hospital Application Update</h2>
			<p>Your application for hospital <b>%s</b> has been reviewed and <b>rejected</b>.</p>
			<p><b>Reason:</b> %s</p>
			<p>You may correct the details and submit a new application via the <b>Hospital MD Signup</b> section in the Ambigo app.</p>
		</div>
	`, hospitalName, reason)
	params := &resend.SendEmailRequest{From: from, To: []string{to}, Subject: subject, Html: html}
	sent, err := m.client.Emails.Send(params)
	if err != nil {
		logger.Log.Error().Err(err).Str("to", to).Str("real_to", toEmail).Msg("Resend reject email failed")
		return err
	}
	logger.Log.Info().Str("id", sent.Id).Str("to", to).Str("real_to", toEmail).Msg("Resend hospital reject sent")
	return nil
}
