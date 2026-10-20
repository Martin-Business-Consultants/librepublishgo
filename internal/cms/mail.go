package cms

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/apierr"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/mail"
)

// ---- email ---------------------------------------------------------------

// Email goes out through the SMTP server in Settings › Email, from its
// default name and address unless a message names its own. The core sends
// its test message with it; plugins granted "mail" send through plugin.API.Mail.
//
//	settings/email: smtp_host, smtp_port, smtp_security (starttls | tls |
//	none), smtp_username, smtp_password (sealed at rest), from_name,
//	from_email, reply_to

// Mailer delivers a built message; SMTP by default, a capture in tests.
type Mailer interface {
	Send(ctx context.Context, cfg mail.Config, from string, to []string, msg []byte) error
}

func (a *App) mailConfig(ctx context.Context) mail.Config {
	s := a.settingOr(ctx, "email")
	port, _ := strconv.Atoi(fmt.Sprint(s["smtp_port"]))
	if f, ok := s["smtp_port"].(float64); ok {
		port = int(f)
	}
	security := str(s["smtp_security"])
	if security == "" {
		security = "starttls"
	}
	if port == 0 {
		port = map[string]int{"tls": 465, "none": 25}[security]
		if port == 0 {
			port = 587
		}
	}
	return mail.Config{
		Host: strings.TrimSpace(str(s["smtp_host"])), Port: port, Security: security,
		Username: str(s["smtp_username"]), Password: str(s["smtp_password"]),
		FromName: str(s["from_name"]), FromEmail: strings.TrimSpace(str(s["from_email"])), ReplyTo: strings.TrimSpace(str(s["reply_to"])),
	}
}

// sendMail checks and sends one message: real addresses only, no header
// injection, at most 50 recipients.
func (a *App) sendMail(ctx context.Context, m mail.Message) error {
	cfg := a.mailConfig(ctx)
	if !cfg.Configured() {
		return fmt.Errorf("email isn't set up: add an SMTP server and a from address in Settings › Email")
	}
	from, rcpt, msg, err := mail.Compose(cfg, m)
	if err != nil {
		return err
	}
	return a.mailer().Send(ctx, cfg, from, rcpt, msg)
}

func (a *App) mailer() Mailer {
	if a.cfg.Mailer != nil {
		return a.cfg.Mailer
	}
	return mail.SMTP{}
}

// ---- the API -------------------------------------------------------------

// sendTestMail sends Settings › Email's test message to the address given
// (or the person asking).
func (a *App) sendTestMail(c *Ctx) error {
	if err := c.Need("settings:write"); err != nil {
		return err
	}
	var body struct {
		To string `json:"to"`
	}
	if err := c.OptionalBody(&body); err != nil {
		return err
	}
	to := body.To
	if to == "" && c.Actor != nil {
		to = c.Actor.Email
	}
	err := a.sendMail(c.R.Context(), mail.Message{
		To: []string{to}, Subject: "Test email from " + strOr(str(a.general(c.R.Context())["title"]), "Libre Publish"),
		Text: "This is a test from Settings › Email. If you can read it, email is working.",
	})
	if err != nil {
		return &apierr.Error{Status: 422, Code: "mail_failed", Message: err.Error()}
	}
	a.audit(c.R.Context(), c.Actor, "email.tested", "Setting", 0, "email", map[string]any{"to": to})
	return c.JSON(200, O("ok", true, "to", to))
}

func strOr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
