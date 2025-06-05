package main

import (
	"fmt"

	"github.com/go-core-stack/core/utils/smtp"
)

func main() {
	config := smtp.Config{
		Host:       "smtp.gmail.com",
		Port:       "587",
		Sender:     "info.psethi@gmail.com",
		SenderName: "Prabhjot Sethi",
		ReplyTo:    "prabhjot.sethi@gmail.com",
		Password:   "ixytccsbvusoivjy",
	}

	client := smtp.New(config)

	err := client.Send(&smtp.Message{
		Receivers: []string{"prabhjot.sethi@gmail.com", "prabhjot.lists@gmail.com"},
		Subject:   "Test Email",
		Body:      "This is a test email sent using the smtp package.",
		Html:      false,
	})

	if err != nil {
		fmt.Printf("failed to send email: %v\n", err)
	}
}
