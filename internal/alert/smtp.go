package alert

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/chaggangamer/tickwatch/internal/store"
)

type smtpConfig struct {
	Host     string   `json:"host"`
	Port     int      `json:"port"`
	Username string   `json:"username"`
	Password string   `json:"password"`
	From     string   `json:"from"`
	To       []string `json:"to"`
}

func (d *Dispatcher) sendSMTP(ch store.Channel, msg Message) error {
	var cfg smtpConfig
	if err := json.Unmarshal(ch.Config, &cfg); err != nil {
		return fmt.Errorf("invalid smtp config: %w", err)
	}
	if cfg.Host == "" || cfg.From == "" || len(cfg.To) == 0 {
		return fmt.Errorf("smtp config requires host, from and to")
	}
	if cfg.Port == 0 {
		cfg.Port = 587
	}
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	conn, err := net.DialTimeout("tcp", addr, 15*time.Second)
	if err != nil {
		return fmt.Errorf("smtp: connect to %s failed", cfg.Host)
	}
	conn.SetDeadline(time.Now().Add(45 * time.Second))
	client, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("smtp: handshake with %s failed", cfg.Host)
	}
	defer client.Close()

	if ok, _ := client.Extension("STARTTLS"); ok {
		if err := client.StartTLS(&tls.Config{ServerName: cfg.Host}); err != nil {
			return fmt.Errorf("smtp: STARTTLS with %s failed", cfg.Host)
		}
	}
	if cfg.Username != "" {
		auth := smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("smtp: authentication with %s failed", cfg.Host)
		}
	}
	if err := client.Mail(cfg.From); err != nil {
		return fmt.Errorf("smtp: sender rejected by %s", cfg.Host)
	}
	for _, rcpt := range cfg.To {
		if err := client.Rcpt(rcpt); err != nil {
			return fmt.Errorf("smtp: recipient rejected by %s", cfg.Host)
		}
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp: data command failed at %s", cfg.Host)
	}
	if _, err := w.Write([]byte(buildEmail(cfg.From, cfg.To, msg))); err != nil {
		w.Close()
		return fmt.Errorf("smtp: writing message failed at %s", cfg.Host)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp: finishing message failed at %s", cfg.Host)
	}
	if err := client.Quit(); err != nil {
		return fmt.Errorf("smtp: quit failed at %s", cfg.Host)
	}
	return nil
}

func buildEmail(from string, to []string, msg Message) string {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(to, ", "))
	fmt.Fprintf(&b, "Subject: %s\r\n", sanitizeHeader(msg.Title))
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	fmt.Fprintf(&b, "MIME-Version: 1.0\r\n")
	fmt.Fprintf(&b, "Content-Type: text/plain; charset=utf-8\r\n\r\n")
	b.WriteString(msg.Body)
	b.WriteString("\r\n")
	return b.String()
}

func sanitizeHeader(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	return strings.ReplaceAll(s, "\n", " ")
}
