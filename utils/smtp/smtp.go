// Copyright © 2025 Prabhjot Singh Sethi, All Rights reserved
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

	message := fmt.Appendf(nil, "Subject: %s\n%s%s", m.Subject, mime, m.Body)
	// Sending email.
	err := smtp.SendMail(c.endpoint, auth, c.config.Sender, m.Receivers, message)
	if err != nil {
		return err
	}

	return nil
}
