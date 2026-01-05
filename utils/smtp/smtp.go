// Copyright © 2025-2026 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

// This is a wrapper over and above the standard net/smtp package
// to enable generic client

package smtp

import (
	"fmt"
	"net/smtp"
)

// Base Configuration with which an smtp client will be created.
type Config struct {
	// smtp server host which is providing the mail services
	// typically gmail smtp would be "smtp.gmail.com"
	Host string

	// port over which the smtp service is provided
	Port string

	// Sender email address / username for authentication
	Sender string

	// Sender Descriptive Name to be included in the email
	SenderName string

	// reply-to email address as typically sender email would
	// be device control and typically would be no-reply,
	// if empty means reply-to is not configured
	ReplyTo string

	// Password for authenticating the sender with smtp server
	Password string
}

// Smtp Client handle, used for triggering sending messages
type Client struct {
	// smtp config object
	config Config

	// smtp endpoint
	endpoint string
}

// Smtp message structure
type Message struct {
	// List of Receivers to whom this message is being sent
	Receivers []string

	// Subject of the message to be sent
	Subject string

	// Body of the email message to be sent
	Body string

	// if this is an HTML message
	Html bool
}

// Create a new Client handle for the given config
func New(config Config) *Client {
	// create smtp endpoint with provided host and port
	return &Client{
		config:   config,
		endpoint: fmt.Sprintf("%s:%s", config.Host, config.Port),
	}
}

func (c *Client) Send(m *Message) error {
	// Authentication.
	auth := smtp.PlainAuth("", c.config.Sender, c.config.Password, c.config.Host)

	mime := ""
	if m.Html {
		mime = "MIME-version: 1.0;\nContent-Type: text/html; charset=\"UTF-8\";\n\n"
	}

	var header string
	if c.config.SenderName != "" {
		// If sender name is provided, use it in the From header.
		header = fmt.Sprintf("From: %s <%s>\r\n", c.config.SenderName, c.config.Sender)
	} else {
		// If sender name is not provided, use only the email address.
		header = fmt.Sprintf("From: <%s>\r\n", c.config.Sender)
	}

	if c.config.ReplyTo != "" {
		// If reply-to is configured, add it to the headers.
		header += fmt.Sprintf("Reply-To: %s\r\n", c.config.ReplyTo)
	}

	if len(m.Receivers) > 0 {
		header += fmt.Sprintf("To: %s", m.Receivers[0])
		for _, receiver := range m.Receivers[1:] {
			header += fmt.Sprintf(", %s", receiver)
		}
		header += "\r\n"
	}

	message := fmt.Appendf(nil, "%sSubject: %s\n%s%s", header, m.Subject, mime, m.Body)
	// Sending email.
	err := smtp.SendMail(c.endpoint, auth, c.config.Sender, m.Receivers, message)
	if err != nil {
		return err
	}

	return nil
}
