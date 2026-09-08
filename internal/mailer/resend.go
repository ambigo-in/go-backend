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
func (m *ResendMailer) SendReceptionistInvite(realEmail, username, tempPassword, hospitalName string) error {
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
	subject := fmt.Sprintf("Your %s receptionist account", hospitalName)
	html := fmt.Sprintf(`
		<div style="font-family: sans-serif; max-width: 600px; margin: 0 auto;">
			<h2 style="color: #FF520D;">Welcome to Ambigo</h2>
			<p>Your receptionist account has been created for <b>%s</b>.</p>
			<table style="background: #f8f8f8; padding: 16px; border-radius: 8px; width: 100%%;">
				<tr><td><b>Username</b></td><td>%s</td></tr>
				<tr><td><b>Temporary password</b></td><td><code style="background: #fff; padding: 4px 8px; border-radius: 4px;">%s</code></td></tr>
				<tr><td><b>Login</b></td><td>Log in via the Hospital section in the Ambigo app using your username and temporary password.</td></tr>
			</table>
			<p style="color: #666; font-size: 12px;">You will be asked to change your password on first login.</p>
			<p style="color: #666; font-size: 12px;">If not in test mode, original recipient: %s</p>
		</div>
	`, hospitalName, username, tempPassword, realEmail)

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
