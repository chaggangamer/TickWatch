package alert

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chaggangamer/tickwatch/internal/store"
)

func newDisp(t *testing.T, reminderEvery time.Duration) (*Dispatcher, *store.DB) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	d := New(s, log)
	d.reminderEvery = reminderEvery
	return d, s.(*store.DB)
}

func setupMonitor(t *testing.T, s *store.DB, hookURL string, cooldown int64) *store.Monitor {
	t.Helper()
	m := &store.Monitor{
		Type: store.TypeHTTP, Name: "web",
		Config:   []byte(`{"url":"https://example.com","method":"GET"}`),
		Interval: 60, Timeout: 10, FailureThreshold: 3, Cooldown: int(cooldown),
		Enabled: true, Status: store.StatusUp,
		CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix(),
		NextRunAt: time.Now().Unix(),
	}
	if err := s.CreateMonitor(m); err != nil {
		t.Fatal(err)
	}
	cfg, _ := json.Marshal(map[string]string{"url": hookURL, "secret": "topsecret"})
	ch := &store.Channel{Type: "webhook", Name: "ops", Config: cfg, CreatedAt: time.Now().Unix()}
	if err := s.CreateChannel(ch); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMonitorChannels(m.ID, []int64{ch.ID}); err != nil {
		t.Fatal(err)
	}
	return m
}

type hookRecorder struct {
	mu       sync.Mutex
	requests []capturedRequest
}

type capturedRequest struct {
	event   string
	secret  string
	body    string
	monitor string
}

func (h *hookRecorder) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.requests)
}

func (h *hookRecorder) last() capturedRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.requests[len(h.requests)-1]
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

func newHook(t *testing.T) (*hookRecorder, string) {
	t.Helper()
	h := &hookRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload struct {
			Event   string `json:"event"`
			Monitor struct {
				Name string `json:"name"`
			} `json:"monitor"`
		}
		_ = json.Unmarshal(body, &payload)
		h.mu.Lock()
		h.requests = append(h.requests, capturedRequest{
			event:   payload.Event,
			secret:  r.Header.Get("X-Tickwatch-Secret"),
			body:    string(body),
			monitor: payload.Monitor.Name,
		})
		h.mu.Unlock()
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)
	return h, srv.URL
}

func startDisp(t *testing.T, d *Dispatcher) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	d.Start(ctx)
	t.Cleanup(cancel)
}

func TestWebhookDownAndRecovery(t *testing.T) {
	h, url := newHook(t)
	d, s := newDisp(t, time.Hour)
	m := setupMonitor(t, s, url, 60)
	startDisp(t, d)

	d.NotifyDown(*m, "connection refused", 1)
	waitFor(t, 3*time.Second, func() bool { return h.count() >= 1 })

	req := h.last()
	if req.event != "down" || req.monitor != "web" || req.secret != "topsecret" {
		t.Errorf("captured = %+v", req)
	}
	if !strings.Contains(req.body, "connection refused") {
		t.Errorf("body missing reason: %s", req.body)
	}

	d.NotifyRecovered(*m, 42)
	waitFor(t, 3*time.Second, func() bool { return h.count() >= 2 })
	if req := h.last(); req.event != "recovered" {
		t.Errorf("second event = %q, want recovered", req.event)
	}
}

func TestCooldownSuppressesSameIncident(t *testing.T) {
	h, url := newHook(t)
	d, s := newDisp(t, time.Hour)
	m := setupMonitor(t, s, url, 300)
	startDisp(t, d)

	d.NotifyDown(*m, "refused", 1)
	waitFor(t, 3*time.Second, func() bool { return h.count() == 1 })

	d.NotifyDown(*m, "refused", 1)
	d.NotifyDown(*m, "refused", 1)
	time.Sleep(300 * time.Millisecond)
	if h.count() != 1 {
		t.Errorf("requests = %d, want 1 (cooldown should suppress)", h.count())
	}
}

func TestNewIncidentOverridesCooldown(t *testing.T) {
	h, url := newHook(t)
	d, s := newDisp(t, time.Hour)
	m := setupMonitor(t, s, url, 300)
	startDisp(t, d)

	d.NotifyDown(*m, "refused", 1)
	waitFor(t, 3*time.Second, func() bool { return h.count() == 1 })

	d.NotifyDown(*m, "refused again", 2)
	waitFor(t, 3*time.Second, func() bool { return h.count() == 2 })
	if req := h.last(); !strings.Contains(req.body, "refused again") {
		t.Errorf("new incident not delivered: %s", req.body)
	}
}

func TestReminderAfterCooldown(t *testing.T) {
	h, url := newHook(t)
	d, s := newDisp(t, 100*time.Millisecond)
	m := setupMonitor(t, s, url, 1)

	m.Status = store.StatusDown
	if err := s.SaveRuntime(m); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenIncident(m.ID, time.Now().Unix()-2, "refused"); err != nil {
		t.Fatal(err)
	}
	startDisp(t, d)

	waitFor(t, 5*time.Second, func() bool { return h.count() >= 2 })
	last := h.last()
	if last.event != "reminder" {
		t.Errorf("last event = %q, want reminder", last.event)
	}
}

func TestSendTest(t *testing.T) {
	h, url := newHook(t)
	d, s := newDisp(t, time.Hour)
	setupMonitor(t, s, url, 60)
	startDisp(t, d)

	channels, _ := s.Channels()
	if err := d.SendTest(channels[0].ID); err != nil {
		t.Fatalf("send test: %v", err)
	}
	waitFor(t, 3*time.Second, func() bool { return h.count() >= 1 })
	if req := h.last(); req.event != "test" {
		t.Errorf("event = %q, want test", req.event)
	}

	if err := d.SendTest(999); err == nil {
		t.Error("test for missing channel should fail")
	}
}

func TestChannelPayloads(t *testing.T) {
	var mu sync.Mutex
	var lastPath, lastBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		lastPath = r.URL.Path
		lastBody = string(body)
		mu.Unlock()
	}))
	defer srv.Close()

	d, s := newDisp(t, time.Hour)
	d.telegramAPI = srv.URL

	tg := &store.Channel{Type: "telegram", Name: "tg", Config: []byte(`{"bot_token":"TOK","chat_id":"42"}`), CreatedAt: 0}
	if err := s.CreateChannel(tg); err != nil {
		t.Fatal(err)
	}
	if err := d.sendTelegram(*tg, Message{Title: "T", Body: "B"}); err != nil {
		t.Fatalf("telegram: %v", err)
	}
	mu.Lock()
	if lastPath != "/botTOK/sendMessage" {
		t.Errorf("telegram path = %q", lastPath)
	}
	mu.Unlock()

	sl := &store.Channel{Type: "slack", Name: "sl", Config: []byte(`{"webhook_url":"` + srv.URL + `"}`), CreatedAt: 0}
	if err := s.CreateChannel(sl); err != nil {
		t.Fatal(err)
	}
	if err := d.sendSlack(*sl, Message{Title: "T", Body: "B"}); err != nil {
		t.Fatalf("slack: %v", err)
	}
	var slackPayload map[string]string
	mu.Lock()
	_ = json.Unmarshal([]byte(lastBody), &slackPayload)
	mu.Unlock()
	if _, ok := slackPayload["text"]; !ok {
		t.Errorf("slack payload missing text: %s", lastBody)
	}

	dc := &store.Channel{Type: "discord", Name: "dc", Config: []byte(`{"webhook_url":"` + srv.URL + `"}`), CreatedAt: 0}
	if err := s.CreateChannel(dc); err != nil {
		t.Fatal(err)
	}
	if err := d.sendDiscord(*dc, Message{Title: "T", Body: "B"}); err != nil {
		t.Fatalf("discord: %v", err)
	}
	var discordPayload map[string]any
	mu.Lock()
	_ = json.Unmarshal([]byte(lastBody), &discordPayload)
	mu.Unlock()
	if _, ok := discordPayload["content"]; !ok {
		t.Errorf("discord payload missing content: %s", lastBody)
	}
}

func TestSMTPFailsOnRefused(t *testing.T) {
	d, s := newDisp(t, time.Hour)
	cfg := map[string]any{"host": "127.0.0.1", "port": 1, "from": "a@b.c", "to": []string{"d@e.f"}}
	raw, _ := json.Marshal(cfg)
	ch := &store.Channel{Type: "smtp", Name: "mail", Config: raw, CreatedAt: 0}
	if err := s.CreateChannel(ch); err != nil {
		t.Fatal(err)
	}
	if err := d.sendSMTP(*ch, Message{Title: "T", Body: "B"}); err == nil {
		t.Error("smtp to closed port should fail")
	}
}

func TestMaskAndMerge(t *testing.T) {
	raw := []byte(`{"url":"https://hook","secret":"real-secret"}`)
	masked := MaskConfig("webhook", raw)
	if masked["secret"] != MaskedValue {
		t.Errorf("secret = %v, want masked", masked["secret"])
	}
	if masked["url"] != "https://hook" {
		t.Errorf("url = %v", masked["url"])
	}

	updated := []byte(`{"url":"https://hook2","secret":"*****"}`)
	merged, err := MergeMaskedConfig("webhook", raw, updated)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]string
	_ = json.Unmarshal(merged, &m)
	if m["secret"] != "real-secret" {
		t.Errorf("merged secret = %q, want real-secret", m["secret"])
	}
	if m["url"] != "https://hook2" {
		t.Errorf("merged url = %q", m["url"])
	}
}

func TestFormatDuration(t *testing.T) {
	cases := map[int64]string{
		0:     "0s",
		45:    "45s",
		125:   "2m 5s",
		3720:  "1h 2m",
		90061: "1d 1h 1m",
	}
	for in, want := range cases {
		if got := formatDuration(in); got != want {
			t.Errorf("formatDuration(%d) = %q, want %q", in, got, want)
		}
	}
}
