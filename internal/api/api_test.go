package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/chaggangamer/tickwatch/internal/alert"
	"github.com/chaggangamer/tickwatch/internal/incident"
	"github.com/chaggangamer/tickwatch/internal/scheduler"
	"github.com/chaggangamer/tickwatch/internal/store"
)

func newAPI(t *testing.T) (http.Handler, *store.DB, *incident.Tracker) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	al := alert.New(s, log)
	tr := incident.New(s, log)
	sched := scheduler.New(scheduler.Deps{
		Store: s, Tracker: tr, Alerts: al, Workers: 2, Log: log,
	})
	d := Deps{Store: s, Alerts: al, Sched: sched, Tracker: tr, BaseURL: "http://localhost:8080", Version: "test", Log: log}
	return New(d), s.(*store.DB), tr
}

func doReq(t *testing.T, h http.Handler, method, path, token string, body any) (*http.Response, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	res := rec.Result()
	var parsed map[string]any
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &parsed)
	}
	return res, parsed
}

func makeToken(t *testing.T, s *store.DB) string {
	t.Helper()
	tok := store.NewToken(32)
	if err := s.CreateAPIToken(&store.APIToken{Name: "test", TokenHash: store.HashToken(tok), CreatedAt: time.Now().Unix()}); err != nil {
		t.Fatal(err)
	}
	return tok
}

func validMonitorBody() map[string]any {
	return map[string]any{
		"type": "http",
		"name": "example",
		"config": map[string]any{
			"url": "https://example.com", "method": "GET",
		},
		"interval_seconds": 60,
	}
}

func TestAuthRequired(t *testing.T) {
	h, _, _ := newAPI(t)

	res, body := doReq(t, h, "GET", "/api/v1/monitors", "", nil)
	if res.StatusCode != 401 {
		t.Errorf("status = %d, want 401", res.StatusCode)
	}
	errObj, ok := body["error"].(map[string]any)
	if !ok || errObj["code"] != "unauthorized" {
		t.Errorf("error shape wrong: %v", body)
	}

	res, _ = doReq(t, h, "GET", "/api/v1/monitors", "wrong-token", nil)
	if res.StatusCode != 401 {
		t.Errorf("status with bad token = %d, want 401", res.StatusCode)
	}
}

func TestMonitorCRUD(t *testing.T) {
	h, s, _ := newAPI(t)
	tok := makeToken(t, s)

	res, body := doReq(t, h, "POST", "/api/v1/monitors", tok, validMonitorBody())
	if res.StatusCode != 201 {
		t.Fatalf("create status = %d, body %v", res.StatusCode, body)
	}
	id := int64(body["id"].(float64))
	if id == 0 {
		t.Fatal("id missing")
	}
	cfg, ok := body["config"].(map[string]any)
	if !ok || cfg["url"] != "https://example.com" {
		t.Errorf("config not echoed: %v", body["config"])
	}

	res, body = doReq(t, h, "GET", "/api/v1/monitors", tok, nil)
	if res.StatusCode != 200 {
		t.Fatalf("list status = %d", res.StatusCode)
	}
	items := body["items"].([]any)
	if len(items) != 1 {
		t.Errorf("items = %d, want 1", len(items))
	}

	res, _ = doReq(t, h, "PUT", "/api/v1/monitors/999", tok, validMonitorBody())
	if res.StatusCode != 404 {
		t.Errorf("update missing status = %d, want 404", res.StatusCode)
	}

	upd := validMonitorBody()
	upd["name"] = "renamed"
	res, body = doReq(t, h, "PUT", "/api/v1/monitors/1", tok, upd)
	if res.StatusCode != 200 {
		t.Fatalf("update status = %d, body %v", res.StatusCode, body)
	}
	if body["name"] != "renamed" {
		t.Errorf("name not updated: %v", body["name"])
	}

	res, _ = doReq(t, h, "DELETE", "/api/v1/monitors/1", tok, nil)
	if res.StatusCode != 204 {
		t.Errorf("delete status = %d, want 204", res.StatusCode)
	}
	res, _ = doReq(t, h, "GET", "/api/v1/monitors/1", tok, nil)
	if res.StatusCode != 404 {
		t.Errorf("get after delete = %d, want 404", res.StatusCode)
	}
}

func TestMonitorValidationErrors(t *testing.T) {
	h, s, _ := newAPI(t)
	tok := makeToken(t, s)

	cases := []struct {
		name string
		body map[string]any
	}{
		{"low interval", map[string]any{"type": "http", "name": "x", "config": map[string]any{"url": "https://a.com"}, "interval_seconds": 5}},
		{"bad type", map[string]any{"type": "dns", "name": "x", "config": map[string]any{}, "interval_seconds": 60}},
		{"bad url", map[string]any{"type": "http", "name": "x", "config": map[string]any{"url": "notaurl"}, "interval_seconds": 60}},
		{"missing name", map[string]any{"type": "http", "config": map[string]any{"url": "https://a.com"}, "interval_seconds": 60}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, body := doReq(t, h, "POST", "/api/v1/monitors", tok, tc.body)
			if res.StatusCode != 400 {
				t.Errorf("status = %d, want 400, body %v", res.StatusCode, body)
			}
			errObj, ok := body["error"].(map[string]any)
			if !ok || (errObj["code"] != "invalid_monitor" && errObj["code"] != "invalid_json") {
				t.Errorf("error code = %v", errObj)
			}
		})
	}
}

func TestResultsPagination(t *testing.T) {
	h, s, _ := newAPI(t)
	tok := makeToken(t, s)

	m := &store.Monitor{
		Type: store.TypeHTTP, Name: "web",
		Config:   []byte(`{"url":"https://example.com"}`),
		Interval: 60, Timeout: 10, FailureThreshold: 3, Cooldown: 300,
		Enabled: true, Status: store.StatusUp,
		CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix(), NextRunAt: time.Now().Unix(),
	}
	store.Normalize(m)
	if err := s.CreateMonitor(m); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 7; i++ {
		if err := s.RecordResult(&store.CheckResult{MonitorID: m.ID, OK: true, LatencyMS: int64(i), CreatedAt: time.Now().Unix()}); err != nil {
			t.Fatal(err)
		}
	}

	res, body := doReq(t, h, "GET", "/api/v1/monitors/1/results?page=2&per_page=3", tok, nil)
	if res.StatusCode != 200 {
		t.Fatalf("status = %d", res.StatusCode)
	}
	items := body["items"].([]any)
	if len(items) != 3 {
		t.Errorf("items = %d, want 3", len(items))
	}
	if body["total"].(float64) != 7 {
		t.Errorf("total = %v, want 7", body["total"])
	}
	if body["page"].(float64) != 2 {
		t.Errorf("page = %v, want 2", body["page"])
	}
	first := items[0].(map[string]any)
	if first["latency_ms"].(float64) != 3 {
		t.Errorf("first latency = %v, want 3 (desc order)", first["latency_ms"])
	}
}

func TestResultsNotFoundMonitor(t *testing.T) {
	h, s, _ := newAPI(t)
	tok := makeToken(t, s)
	res, _ := doReq(t, h, "GET", "/api/v1/monitors/42/results", tok, nil)
	if res.StatusCode != 404 {
		t.Errorf("status = %d, want 404", res.StatusCode)
	}
}

func TestChannelEndpoints(t *testing.T) {
	h, s, _ := newAPI(t)
	tok := makeToken(t, s)

	body := map[string]any{
		"type":   "webhook",
		"name":   "ops",
		"config": map[string]any{"url": "https://example.com/hook", "secret": "s3cret"},
	}
	res, created := doReq(t, h, "POST", "/api/v1/channels", tok, body)
	if res.StatusCode != 201 {
		t.Fatalf("create status = %d, body %v", res.StatusCode, created)
	}
	cfg := created["config"].(map[string]any)
	if cfg["secret"] != "*****" {
		t.Errorf("secret not masked: %v", cfg)
	}

	res, got := doReq(t, h, "GET", "/api/v1/channels/1", tok, nil)
	if res.StatusCode != 200 {
		t.Fatalf("get status = %d", res.StatusCode)
	}
	if got["config"].(map[string]any)["secret"] != "*****" {
		t.Error("get leaks secret")
	}

	upd := map[string]any{
		"type":   "webhook",
		"name":   "ops2",
		"config": map[string]any{"url": "https://example.com/hook2", "secret": "*****"},
	}
	res, _ = doReq(t, h, "PUT", "/api/v1/channels/1", tok, upd)
	if res.StatusCode != 200 {
		t.Fatalf("update status = %d", res.StatusCode)
	}
	raw, _ := s.Channel(1)
	var stored map[string]string
	_ = json.Unmarshal(raw.Config, &stored)
	if stored["secret"] != "s3cret" {
		t.Errorf("masked secret not merged from existing: %v", stored)
	}
	if stored["url"] != "https://example.com/hook2" {
		t.Errorf("url not updated: %v", stored)
	}

	res, _ = doReq(t, h, "DELETE", "/api/v1/channels/1", tok, nil)
	if res.StatusCode != 204 {
		t.Errorf("delete status = %d", res.StatusCode)
	}
	res, _ = doReq(t, h, "POST", "/api/v1/channels/1/test", tok, nil)
	if res.StatusCode != 404 {
		t.Errorf("test missing channel = %d, want 404", res.StatusCode)
	}
}

func TestTestChannelSend(t *testing.T) {
	h, s, _ := newAPI(t)
	tok := makeToken(t, s)

	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
	}))
	defer srv.Close()

	body := map[string]any{"type": "webhook", "name": "ops", "config": map[string]any{"url": srv.URL}}
	res, _ := doReq(t, h, "POST", "/api/v1/channels", tok, body)
	if res.StatusCode != 201 {
		t.Fatalf("create status = %d", res.StatusCode)
	}

	res, respBody := doReq(t, h, "POST", "/api/v1/channels/1/test", tok, nil)
	if res.StatusCode != 200 {
		t.Errorf("test status = %d, body %v", res.StatusCode, respBody)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && hits == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	if hits == 0 {
		t.Error("webhook was not called")
	}
}

func TestHeartbeatPingAndPingURL(t *testing.T) {
	h, s, _ := newAPI(t)
	tok := makeToken(t, s)

	body := map[string]any{
		"type": "heartbeat", "name": "nightly",
		"config": map[string]any{"period": 3600, "grace": 300},
	}
	res, created := doReq(t, h, "POST", "/api/v1/monitors", tok, body)
	if res.StatusCode != 201 {
		t.Fatalf("create status = %d, body %v", res.StatusCode, created)
	}
	pingURL, ok := created["ping_url"].(string)
	if !ok || pingURL == "" {
		t.Fatalf("ping_url missing: %v", created)
	}
	token := created["heartbeat_token"].(string)

	plain := httptest.NewRequest("GET", "/ping/"+token, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, plain)
	if rec.Code != 200 {
		t.Errorf("ping status = %d", rec.Code)
	}

	res, _ = doReq(t, h, "GET", "/ping/bogus-token", "", nil)
	if res.StatusCode != 404 {
		t.Errorf("bogus token = %d, want 404", res.StatusCode)
	}

	m, _ := s.Monitor(int64(created["id"].(float64)))
	if m.LastPingAt == 0 {
		t.Error("last ping not recorded")
	}
}

func TestIncidentsEndpoint(t *testing.T) {
	h, s, _ := newAPI(t)
	tok := makeToken(t, s)

	m := &store.Monitor{
		Type: store.TypeHTTP, Name: "web",
		Config:   []byte(`{"url":"https://example.com"}`),
		Interval: 60, Timeout: 10, FailureThreshold: 3, Cooldown: 300,
		Enabled: true, Status: store.StatusUp,
		CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix(), NextRunAt: time.Now().Unix(),
	}
	store.Normalize(m)
	if err := s.CreateMonitor(m); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenIncident(m.ID, time.Now().Unix()-60, "refused"); err != nil {
		t.Fatal(err)
	}

	res, body := doReq(t, h, "GET", "/api/v1/incidents", tok, nil)
	if res.StatusCode != 200 {
		t.Fatalf("status = %d", res.StatusCode)
	}
	items := body["items"].([]any)
	if len(items) != 1 {
		t.Errorf("items = %d, want 1", len(items))
	}
	first := items[0].(map[string]any)
	if first["ended_at"].(float64) != 0 {
		t.Errorf("open incident should have ended_at 0, got %v", first["ended_at"])
	}

	res, _ = doReq(t, h, "GET", "/api/v1/monitors/1/incidents", tok, nil)
	if res.StatusCode != 200 {
		t.Errorf("monitor incidents status = %d", res.StatusCode)
	}
}

func TestHealthz(t *testing.T) {
	h, _, _ := newAPI(t)
	res, body := doReq(t, h, "GET", "/healthz", "", nil)
	if res.StatusCode != 200 {
		t.Errorf("status = %d", res.StatusCode)
	}
	if body["status"] != "ok" {
		t.Errorf("body = %v", body)
	}
}

func TestInvalidID(t *testing.T) {
	h, s, _ := newAPI(t)
	tok := makeToken(t, s)
	res, _ := doReq(t, h, "GET", "/api/v1/monitors/abc", tok, nil)
	if res.StatusCode != 400 {
		t.Errorf("status = %d, want 400", res.StatusCode)
	}
	res, _ = doReq(t, h, "GET", "/api/v1/monitors/0", tok, nil)
	if res.StatusCode != 400 {
		t.Errorf("status = %d, want 400", res.StatusCode)
	}
}

func TestAPIUpdateResetsTrackerForDownMonitor(t *testing.T) {
	h, s, tr := newAPI(t)
	tok := makeToken(t, s)

	m := &store.Monitor{
		Type: store.TypeHTTP, Name: "down-one",
		Config:   []byte(`{"url":"https://example.com"}`),
		Interval: 60, Timeout: 10, FailureThreshold: 1, Cooldown: 300,
		Enabled: true, Status: store.StatusDown,
		CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix(), NextRunAt: time.Now().Unix(),
	}
	store.Normalize(m)
	if err := s.CreateMonitor(m); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenIncident(m.ID, time.Now().Unix()-60, "refused"); err != nil {
		t.Fatal(err)
	}

	upd := validMonitorBody()
	upd["name"] = "down-one-renamed"
	res, _ := doReq(t, h, "PUT", "/api/v1/monitors/1", tok, upd)
	if res.StatusCode != 200 {
		t.Fatalf("update status = %d", res.StatusCode)
	}
	if !tr.IsDown(m.ID) {
		t.Fatal("after api update on a DOWN monitor the tracker should be down again")
	}
	ev := tr.Observe(m, true, "", time.Now().Unix()+30)
	if ev == nil || ev.Kind != incident.EventRecovered {
		t.Fatalf("success after api update should recover, got %+v", ev)
	}
	if m.Status != store.StatusUp {
		t.Errorf("status = %s, want up", m.Status)
	}
	if open, _ := s.OpenIncidents(); len(open) != 0 {
		t.Errorf("incident should be closed, got %d open", len(open))
	}
}
