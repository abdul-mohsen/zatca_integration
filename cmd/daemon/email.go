package main

import (
	"fmt"
	"log"
	"net/smtp"
	"os"
	"strings"

	"github.com/zatca-go/zatca/queue"
)

// sendAlert sends a failure notification email.
// recipients is a comma-separated list of email addresses from the tenant config.
func sendAlert(recipients string, msg queue.Message, processErr error) {
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

	subject := fmt.Sprintf("ZATCA Processing Failed: %s id=%d (%s)", msg.DocType, msg.ID, msg.DBName)
	body := fmt.Sprintf(
		"ZATCA document processing failed after %d retries.\n\n"+
			"Tenant DB: %s\n"+
			"Document Type: %s\n"+
			"Document ID: %d\n"+
			"Branch ID: %d\n"+
			"Error: %v\n\n"+
			"The document remains in a pending state and needs manual attention.\n"+
			"You can reprocess it using the CLI tool or restart the daemon to trigger replay.",
		maxRetries, msg.DBName, msg.DocType, msg.ID, msg.BranchID, processErr,
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
		log.Printf("Alert email sent to %s for %s id=%d", recipients, msg.DocType, msg.ID)
	}
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
