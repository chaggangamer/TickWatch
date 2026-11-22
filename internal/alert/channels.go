package alert

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/chaggangamer/tickwatch/internal/store"
)

func (d *Dispatcher) sendTelegram(ch store.Channel, msg Message) error {
	var cfg struct {
		BotToken string `json:"bot_token"`
		ChatID   string `json:"chat_id"`
	}
	if err := json.Unmarshal(ch.Config, &cfg); err != nil {
		return fmt.Errorf("invalid telegram config: %w", err)
	}
	if cfg.BotToken == "" || cfg.ChatID == "" {
		return fmt.Errorf("telegram config requires bot_token and chat_id")
	}
	url := fmt.Sprintf("%s/bot%s/sendMessage", d.telegramAPI, cfg.BotToken)
	payload := map[string]string{
		"chat_id": cfg.ChatID,
		"text":    msg.Title + "\n\n" + msg.Body,
	}
	return postJSON(d.client, url, payload, nil, "send telegram")
}

func (d *Dispatcher) sendSlack(ch store.Channel, msg Message) error {
	var cfg struct {
		WebhookURL string `json:"webhook_url"`
	}
	if err := json.Unmarshal(ch.Config, &cfg); err != nil {
		return fmt.Errorf("invalid slack config: %w", err)
	}
	if cfg.WebhookURL == "" {
		return fmt.Errorf("slack config requires webhook_url")
	}
	payload := map[string]string{"text": msg.Title + "\n" + msg.Body}
	return postJSON(d.client, cfg.WebhookURL, payload, nil, "send slack")
}

func (d *Dispatcher) sendDiscord(ch store.Channel, msg Message) error {
	var cfg struct {
		WebhookURL string `json:"webhook_url"`
	}
	if err := json.Unmarshal(ch.Config, &cfg); err != nil {
		return fmt.Errorf("invalid discord config: %w", err)
	}
	if cfg.WebhookURL == "" {
		return fmt.Errorf("discord config requires webhook_url")
	}
	payload := map[string]any{
		"content":  msg.Title + "\n" + msg.Body,
		"username": "Tickwatch",
	}
	return postJSON(d.client, cfg.WebhookURL, payload, nil, "send discord")
}

type webhookConfig struct {
	URL     string            `json:"url"`
	Secret  string            `json:"secret"`
	Headers map[string]string `json:"headers"`
}

func (d *Dispatcher) sendWebhook(ch store.Channel, msg Message, ev event) error {
	var cfg webhookConfig
	if err := json.Unmarshal(ch.Config, &cfg); err != nil {
		return fmt.Errorf("invalid webhook config: %w", err)
	}
	if cfg.URL == "" {
		return fmt.Errorf("webhook config requires url")
	}
	payload := map[string]any{
		"event":   ev.kind,
		"time":    time.Now().Unix(),
		"message": msg.Title + "\n" + msg.Body,
	}
	if ev.monitor.ID != 0 {
		payload["monitor"] = map[string]any{
			"id":     ev.monitor.ID,
			"name":   ev.monitor.Name,
			"type":   ev.monitor.Type,
			"target": ev.monitor.Target(),
		}
	}
	headers := map[string]string{"X-Tickwatch-Event": ev.kind}
	if cfg.Secret != "" {
		headers["X-Tickwatch-Secret"] = cfg.Secret
	}
	for k, v := range cfg.Headers {
		headers[k] = v
	}
	return postJSON(d.client, cfg.URL, payload, headers, "send webhook")
}

func safeHTTPError(operation, target string, err error) error {
	u, parseErr := url.Parse(target)
	host := "unknown host"
	if parseErr == nil && u.Hostname() != "" {
		host = u.Hostname()
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return fmt.Errorf("%s: request to %s failed: %w", operation, host, urlErr.Err)
	}
	return fmt.Errorf("%s: request to %s failed", operation, host)
}

func postJSON(client *http.Client, target string, payload any, headers map[string]string, operation string) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build webhook request: invalid target")
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return safeHTTPError(operation, target, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%s: request to target returned status %d", operation, resp.StatusCode)
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return nil
}

const MaskedValue = "*****"

var secretFields = map[string][]string{
	"telegram": {"bot_token"},
	"slack":    {"webhook_url"},
	"discord":  {"webhook_url"},
	"smtp":     {"password"},
	"webhook":  {"secret"},
}

func MaskConfig(chType string, cfg []byte) map[string]any {
	var m map[string]any
	if err := json.Unmarshal(cfg, &m); err != nil || m == nil {
		m = map[string]any{}
	}
	for _, key := range secretFields[chType] {
		if v, ok := m[key].(string); ok && v != "" {
			m[key] = MaskedValue
		}
	}
	return m
}

func MergeMaskedConfig(chType string, old, newCfg []byte) ([]byte, error) {
	var newMap map[string]any
	if err := json.Unmarshal(newCfg, &newMap); err != nil {
		return nil, fmt.Errorf("invalid channel config: %w", err)
	}
	if newMap == nil {
		newMap = map[string]any{}
	}
	var oldMap map[string]any
	_ = json.Unmarshal(old, &oldMap)
	for _, key := range secretFields[chType] {
		if v, ok := newMap[key].(string); ok && v == MaskedValue {
			if oldMap != nil {
				if ov, exists := oldMap[key]; exists {
					newMap[key] = ov
				}
			}
		}
	}
	return json.Marshal(newMap)
}
