package main

import (
	"fmt"
	"log"
	"net/smtp"
	"os"
	"strings"
)

// sendAlert sends a failure notification email about one item of a tenant: a bill, or an onboarding.
// recipients is a comma-separated list of email addresses from the tenant config.
func sendAlert(recipients, what, dbName string, processErr error) {
	if recipients == "" {
		return
	}

	// Use environment variables or defaults for SMTP config.
	// In production, these should be set as daemon flags or env vars.
	smtpHost := envOrDefault("SMTP_HOST", "localhost")
	smtpPort := envOrDefault("SMTP_PORT", "587")
	smtpUser := envOrDefault("SMTP_USER", "")
	smtpPass := envOrDefault("SMTP_PASS", "")
	fromAddr := envOrDefault("SMTP_FROM", "zatca-daemon@localhost")

	subject := fmt.Sprintf("ZATCA Processing Failed: %s (%s)", what, dbName)
	body := fmt.Sprintf(
		"ZATCA processing failed.\n\n"+
			"Tenant DB: %s\n"+
			"Item: %s\n"+
			"Error: %v\n\n"+
			"The item is not invoiced or not submitted. Fix the cause; a bill that was not invoiced is retried by the daemon.",
		dbName, what, processErr,
	)

	toList := strings.Split(recipients, ",")
	for i := range toList {
		toList[i] = strings.TrimSpace(toList[i])
	}

	emailBody := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\n\r\n%s",
		fromAddr, strings.Join(toList, ", "), subject, body)

	var auth smtp.Auth
	if smtpUser != "" {
		auth = smtp.PlainAuth("", smtpUser, smtpPass, smtpHost)
	}

	addr := smtpHost + ":" + smtpPort
	if err := smtp.SendMail(addr, auth, fromAddr, toList, []byte(emailBody)); err != nil {
		log.Printf("WARN: failed to send alert email to %s: %v", recipients, err)
	} else {
		log.Printf("Alert email sent to %s for %s", recipients, what)
	}
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
