package store

import (
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"
)

func testStore(t *testing.T) *sqliteStore {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "test.db"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st.(*sqliteStore)
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func newHTTPMonitor(t *testing.T, name, url string) *Monitor {
	t.Helper()
	m := &Monitor{
		Type:    TypeHTTP,
		Name:    name,
		Config:  mustJSON(t, HTTPConfig{URL: url, Method: "GET", ExpectStatus: "200-299"}),
		Enabled: true,
	}
	Normalize(m)
	if err := m.Validate(); err != nil {
		t.Fatalf("validate monitor: %v", err)
	}
	return m
}

func TestMonitorCRUD(t *testing.T) {
	s := testStore(t)

	m := newHTTPMonitor(t, "example", "https://example.com")
	if err := s.CreateMonitor(m); err != nil {
		t.Fatalf("create: %v", err)
	}
	if m.ID == 0 {
		t.Fatal("id not set")
	}

	got, err := s.Monitor(m.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "example" || got.Type != TypeHTTP || got.Interval != 60 {
		t.Errorf("unexpected monitor: %+v", got)
	}
	var cfg HTTPConfig
	if err := json.Unmarshal(got.Config, &cfg); err != nil {
		t.Fatalf("config roundtrip: %v", err)
	}
	if cfg.URL != "https://example.com" {
		t.Errorf("config url = %q", cfg.URL)
	}

	got.Name = "renamed"
	got.Interval = 120
	if err := s.UpdateMonitor(got); err != nil {
		t.Fatalf("update: %v", err)
	}
	again, _ := s.Monitor(m.ID)
	if again.Name != "renamed" || again.Interval != 120 {
		t.Errorf("update not persisted: %+v", again)
	}

	if _, err := s.Monitor(99999); err != ErrNotFound {
		t.Errorf("missing monitor err = %v, want ErrNotFound", err)
	}

	due, err := s.DueMonitors(time.Now().Unix()+10, 10)
	if err != nil {
		t.Fatalf("due: %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("due monitors = %d, want 1", len(due))
	}

	if err := s.DeleteMonitor(m.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.Monitor(m.ID); err != ErrNotFound {
		t.Errorf("deleted monitor err = %v, want ErrNotFound", err)
	}
}

func TestMonitorValidation(t *testing.T) {
	cases := []struct {
		name    string
		monitor *Monitor
		wantErr bool
	}{
		{"valid http", newHTTPMonitor(t, "ok", "https://example.com"), false},
		{"empty name", func() *Monitor { m := newHTTPMonitor(t, "x", "https://example.com"); m.Name = ""; return m }(), true},
		{"long name", func() *Monitor { m := newHTTPMonitor(t, "x", "https://example.com"); m.Name = string(make([]byte, 200)); return m }(), true},
		{"bad type", func() *Monitor { m := newHTTPMonitor(t, "x", "https://example.com"); m.Type = "gopher"; return m }(), true},
		{"interval too low", func() *Monitor { m := newHTTPMonitor(t, "x", "https://example.com"); m.Interval = 10; return m }(), true},
		{"interval ok", func() *Monitor { m := newHTTPMonitor(t, "x", "https://example.com"); m.Interval = 20; return m }(), false},
		{"timeout too high", func() *Monitor { m := newHTTPMonitor(t, "x", "https://example.com"); m.Timeout = 500; return m }(), true},
		{"threshold zero", func() *Monitor { m := newHTTPMonitor(t, "x", "https://example.com"); m.FailureThreshold = 0; return m }(), true},
		{"ftp url", func() *Monitor {
			m := newHTTPMonitor(t, "x", "https://example.com")
			m.Config = mustJSON(t, HTTPConfig{URL: "ftp://example.com"})
			return m
		}(), true},
		{"bad method", func() *Monitor {
			m := newHTTPMonitor(t, "x", "https://example.com")
			m.Config = mustJSON(t, HTTPConfig{URL: "https://example.com", Method: "TRACE"})
			return m
		}(), true},
		{"bad expect status", func() *Monitor {
			m := newHTTPMonitor(t, "x", "https://example.com")
			m.Config = mustJSON(t, HTTPConfig{URL: "https://example.com", ExpectStatus: "200-99"})
			return m
		}(), true},
		{"bad regex", func() *Monitor {
			m := newHTTPMonitor(t, "x", "https://example.com")
			m.Config = mustJSON(t, HTTPConfig{URL: "https://example.com", BodyRegex: "(["})
			return m
		}(), true},
		{"match and regex", func() *Monitor {
			m := newHTTPMonitor(t, "x", "https://example.com")
			m.Config = mustJSON(t, HTTPConfig{URL: "https://example.com", BodyMatch: "a", BodyRegex: "b"})
			return m
		}(), true},
		{"bad header value", func() *Monitor {
			m := newHTTPMonitor(t, "x", "https://example.com")
			m.Config = mustJSON(t, HTTPConfig{URL: "https://example.com", Headers: map[string]string{"X-A": "bad\nvalue"}})
			return m
		}(), true},
		{"valid tcp", func() *Monitor {
			m := &Monitor{Type: TypeTCP, Name: "t", Config: mustJSON(t, TCPConfig{Host: "example.com", Port: 443})}
			Normalize(m)
			return m
		}(), false},
		{"tcp port range", func() *Monitor {
			m := &Monitor{Type: TypeTCP, Name: "t", Config: mustJSON(t, TCPConfig{Host: "example.com", Port: 70000})}
			Normalize(m)
			return m
		}(), true},
		{"bad tcp host", func() *Monitor {
			m := &Monitor{Type: TypeTCP, Name: "t", Config: mustJSON(t, TCPConfig{Host: "bad host!", Port: 80})}
			Normalize(m)
			return m
		}(), true},
		{"valid ping", func() *Monitor {
			m := &Monitor{Type: TypePing, Name: "p", Config: mustJSON(t, PingConfig{Host: "1.1.1.1"})}
			Normalize(m)
			return m
		}(), false},
		{"valid heartbeat", func() *Monitor {
			m := &Monitor{Type: TypeHeartbeat, Name: "h", Config: mustJSON(t, HeartbeatConfig{Period: 3600, Grace: 300})}
			Normalize(m)
			return m
		}(), false},
		{"heartbeat period low", func() *Monitor {
			m := &Monitor{Type: TypeHeartbeat, Name: "h", Config: mustJSON(t, HeartbeatConfig{Period: 10})}
			Normalize(m)
			return m
		}(), true},
		{"heartbeat bad grace", func() *Monitor {
			m := &Monitor{Type: TypeHeartbeat, Name: "h", Config: mustJSON(t, HeartbeatConfig{Period: 3600, Grace: -5})}
			Normalize(m)
			return m
		}(), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.monitor.Validate()
			if tc.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestParseStatusRanges(t *testing.T) {
	cases := []struct {
		in     string
		code   int
		expect bool
		err    bool
	}{
		{"", 200, true, false},
		{"", 500, false, false},
		{"200", 200, true, false},
		{"200", 201, false, false},
		{"200-299", 250, true, false},
		{"200,301", 301, true, false},
		{"200-204,300", 203, true, false},
		{"99", 99, false, true},
		{"200-100", 150, false, true},
		{"abc", 200, false, true},
	}
	for _, tc := range cases {
		ranges, err := ParseStatusRanges(tc.in)
		if tc.err {
			if err == nil {
				t.Errorf("%q: expected error", tc.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", tc.in, err)
			continue
		}
		found := false
		for _, r := range ranges {
			if tc.code >= r[0] && tc.code <= r[1] {
				found = true
			}
		}
		if found != tc.expect {
			t.Errorf("%q with code %d: match=%v, want %v", tc.in, tc.code, found, tc.expect)
		}
	}
}

func TestResultsAndUptime(t *testing.T) {
	s := testStore(t)
	m := newHTTPMonitor(t, "example", "https://example.com")
	if err := s.CreateMonitor(m); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	for i := 0; i < 5; i++ {
		r := &CheckResult{MonitorID: m.ID, OK: i != 2, StatusCode: 200, LatencyMS: int64(100 + i), CreatedAt: now - int64(5-i)}
		if err := s.RecordResult(r); err != nil {
			t.Fatal(err)
		}
	}

	items, total, err := s.Results(m.ID, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	if total != 5 {
		t.Errorf("total = %d, want 5", total)
	}
	if len(items) != 3 {
		t.Errorf("page items = %d, want 3", len(items))
	}
	if items[0].LatencyMS != 104 {
		t.Errorf("newest first violated: %d", items[0].LatencyMS)
	}

	recent, err := s.RecentResults(m.ID, 2)
	if err != nil || len(recent) != 2 {
		t.Fatalf("recent = %v, err %v", recent, err)
	}

	latest, err := s.LatestResults()
	if err != nil {
		t.Fatal(err)
	}
	if latest[m.ID].LatencyMS != 104 {
		t.Errorf("latest = %d, want 104", latest[m.ID].LatencyMS)
	}

	stat, err := s.Uptime(m.ID, now-100)
	if err != nil {
		t.Fatal(err)
	}
	if stat.Total != 5 || stat.OK != 4 {
		t.Errorf("uptime = %d/%d, want 4/5", stat.OK, stat.Total)
	}
	if p := stat.Percent(); p != 80 {
		t.Errorf("percent = %v, want 80", p)
	}

	batch, err := s.UptimeBatch(now - 100)
	if err != nil {
		t.Fatal(err)
	}
	if batch[m.ID].Total != 5 {
		t.Errorf("batch total = %d, want 5", batch[m.ID].Total)
	}

	series, err := s.Series(m.ID, now-100, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 4 {
		t.Errorf("series points = %d, want 4 (ok only)", len(series))
	}

	if _, err := s.Uptime(99999, now); err != nil {
		t.Errorf("uptime for missing monitor should not error: %v", err)
	}
}

func TestSeriesWithSummaries(t *testing.T) {
	s := testStore(t)
	m := newHTTPMonitor(t, "example", "https://example.com")
	if err := s.CreateMonitor(m); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	hour := (now / 3600) * 3600

	res := &CheckResult{MonitorID: m.ID, OK: true, LatencyMS: 50, CreatedAt: now - 30}
	if err := s.RecordResult(res); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO hourly_summaries (monitor_id, hour, total, ok, latency_sum) VALUES (?,?,?,?,?)`,
		m.ID, hour-7200, 10, 9, 2000); err != nil {
		t.Fatal(err)
	}

	pts, err := s.Series(m.ID, now-86400, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 2 {
		t.Fatalf("points = %d, want 2", len(pts))
	}
	if pts[0].MS != 200 {
		t.Errorf("summary avg = %d, want 200", pts[0].MS)
	}
	if pts[1].MS != 50 {
		t.Errorf("raw point = %d, want 50", pts[1].MS)
	}
}

func TestIncidents(t *testing.T) {
	s := testStore(t)
	m := newHTTPMonitor(t, "example", "https://example.com")
	if err := s.CreateMonitor(m); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()

	id, err := s.OpenIncident(m.ID, now, "connection refused")
	if err != nil {
		t.Fatal(err)
	}
	if id == 0 {
		t.Fatal("incident id not set")
	}
	open, err := s.OpenIncidents()
	if err != nil || len(open) != 1 {
		t.Fatalf("open incidents = %d, err %v", len(open), err)
	}
	if !open[0].Open() {
		t.Error("incident should be open")
	}

	if err := s.CloseIncidents(m.ID, now+120); err != nil {
		t.Fatal(err)
	}
	open, _ = s.OpenIncidents()
	if len(open) != 0 {
		t.Errorf("open incidents after close = %d, want 0", len(open))
	}

	items, total, err := s.Incidents(m.ID, 1, 10)
	if err != nil || total != 1 {
		t.Fatalf("incidents total = %d, err %v", total, err)
	}
	if items[0].Duration() != 120 {
		t.Errorf("duration = %d, want 120", items[0].Duration())
	}
}

func TestChannelsAndLinks(t *testing.T) {
	s := testStore(t)
	m := newHTTPMonitor(t, "example", "https://example.com")
	if err := s.CreateMonitor(m); err != nil {
		t.Fatal(err)
	}
	ch := &Channel{Type: "slack", Name: "ops", Config: []byte(`{"webhook_url":"https://hooks.slack.com/x"}`), CreatedAt: time.Now().Unix()}
	if err := s.CreateChannel(ch); err != nil {
		t.Fatal(err)
	}
	if ch.ID == 0 {
		t.Fatal("channel id not set")
	}

	if err := s.SetMonitorChannels(m.ID, []int64{ch.ID}); err != nil {
		t.Fatal(err)
	}
	linked, err := s.MonitorChannels(m.ID)
	if err != nil || len(linked) != 1 {
		t.Fatalf("monitor channels = %d, err %v", len(linked), err)
	}

	if err := s.SetMonitorChannels(m.ID, nil); err != nil {
		t.Fatal(err)
	}
	linked, _ = s.MonitorChannels(m.ID)
	if len(linked) != 0 {
		t.Errorf("channels after clear = %d, want 0", len(linked))
	}

	if _, err := s.Channel(99999); err != ErrNotFound {
		t.Errorf("missing channel err = %v", err)
	}
	if err := s.DeleteChannel(ch.ID); err != nil {
		t.Fatal(err)
	}
}

func TestAlertState(t *testing.T) {
	s := testStore(t)
	m := newHTTPMonitor(t, "example", "https://example.com")
	if err := s.CreateMonitor(m); err != nil {
		t.Fatal(err)
	}
	ch := &Channel{Type: "webhook", Name: "ops", Config: []byte(`{"url":"https://example.com/hook"}`), CreatedAt: time.Now().Unix()}
	if err := s.CreateChannel(ch); err != nil {
		t.Fatal(err)
	}
	_, _, ok, err := s.LastAlert(m.ID, ch.ID)
	if err != nil || ok {
		t.Fatalf("initial alert state ok=%v err=%v", ok, err)
	}
	if err := s.SaveAlert(m.ID, ch.ID, 100, 7); err != nil {
		t.Fatal(err)
	}
	at, inc, ok, _ := s.LastAlert(m.ID, ch.ID)
	if !ok || at != 100 || inc != 7 {
		t.Fatalf("alert at=%d incident=%d ok=%v", at, inc, ok)
	}
	if err := s.SaveAlert(m.ID, ch.ID, 200, 7); err != nil {
		t.Fatal(err)
	}
	at, inc, _, _ = s.LastAlert(m.ID, ch.ID)
	if at != 200 || inc != 7 {
		t.Errorf("alert after update = %d/%d, want 200/7", at, inc)
	}
}

func TestSSLAlerts(t *testing.T) {
	s := testStore(t)
	m := newHTTPMonitor(t, "example", "https://example.com")
	if err := s.CreateMonitor(m); err != nil {
		t.Fatal(err)
	}
	m.SSLExpiresAt = time.Now().AddDate(0, 0, 10).Unix()
	if err := s.SaveRuntime(m); err != nil {
		t.Fatal(err)
	}
	targets, err := s.SSLAlertTargets()
	if err != nil || len(targets) != 1 {
		t.Fatalf("targets = %d, err %v", len(targets), err)
	}

	if ok, _ := s.SSLAlerted(m.ID, 14); ok {
		t.Error("should not be alerted yet")
	}
	if err := s.MarkSSLAlert(m.ID, 14, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.SSLAlerted(m.ID, 14); !ok {
		t.Error("should be alerted")
	}
	if err := s.ClearSSLAlerts(m.ID, 20); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.SSLAlerted(m.ID, 14); ok {
		t.Error("alert should be cleared (14 < 20)")
	}
}

func TestUsersSessionsTokens(t *testing.T) {
	s := testStore(t)

	if n, _ := s.UserCount(); n != 0 {
		t.Fatalf("user count = %d, want 0", n)
	}
	u := &User{Username: "admin", PasswordHash: "$2a$10$hash", CreatedAt: time.Now().Unix()}
	if err := s.CreateUser(u); err != nil {
		t.Fatal(err)
	}
	got, err := s.UserByName("admin")
	if err != nil || got.Username != "admin" {
		t.Fatalf("user = %+v err %v", got, err)
	}
	if _, err := s.UserByName("nope"); err != ErrNotFound {
		t.Errorf("missing user err = %v", err)
	}

	sess := &Session{TokenHash: HashToken("cookie1"), UserID: u.ID, CSRFToken: "csrf1", ExpiresAt: time.Now().Unix() + 3600}
	if err := s.CreateSession(sess); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.Session(sess.TokenHash)
	if err != nil || loaded.CSRFToken != "csrf1" {
		t.Fatalf("session = %+v err %v", loaded, err)
	}
	if _, err := s.Session("missing"); err != ErrNotFound {
		t.Errorf("missing session err = %v", err)
	}

	tok := &APIToken{Name: "ci", TokenHash: HashToken("tok1"), CreatedAt: time.Now().Unix()}
	if err := s.CreateAPIToken(tok); err != nil {
		t.Fatal(err)
	}
	if err := s.TouchAPIToken(tok.TokenHash, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	lt, err := s.APITokenByHash(tok.TokenHash)
	if err != nil || lt.LastUsedAt == 0 {
		t.Fatalf("token after touch = %+v err %v", lt, err)
	}
	list, _ := s.APITokens()
	if len(list) != 1 {
		t.Errorf("tokens = %d, want 1", len(list))
	}
	if err := s.DeleteAPIToken(tok.ID); err != nil {
		t.Fatal(err)
	}

	exp := &Session{TokenHash: HashToken("cookie2"), UserID: u.ID, CSRFToken: "c2", ExpiresAt: time.Now().Unix() - 100}
	if err := s.CreateSession(exp); err != nil {
		t.Fatal(err)
	}
	n, err := s.DeleteExpiredSessions(time.Now().Unix())
	if err != nil || n != 1 {
		t.Fatalf("expired deleted = %d err %v", n, err)
	}
}

func TestCascadeDelete(t *testing.T) {
	s := testStore(t)
	m := newHTTPMonitor(t, "example", "https://example.com")
	if err := s.CreateMonitor(m); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordResult(&CheckResult{MonitorID: m.ID, OK: true, CreatedAt: time.Now().Unix()}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenIncident(m.ID, time.Now().Unix(), "boom"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteMonitor(m.ID); err != nil {
		t.Fatal(err)
	}
	items, total, _ := s.Results(m.ID, 1, 10)
	if total != 0 || len(items) != 0 {
		t.Errorf("results not cascaded: %d/%d", len(items), total)
	}
	open, _ := s.OpenIncidents()
	if len(open) != 0 {
		t.Errorf("incidents not cascaded: %d", len(open))
	}
}

func TestRetention(t *testing.T) {
	s := testStore(t)
	m := newHTTPMonitor(t, "example", "https://example.com")
	if err := s.CreateMonitor(m); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	old := now - 40*86400
	for i := 0; i < 3; i++ {
		if err := s.RecordResult(&CheckResult{MonitorID: m.ID, OK: i != 1, LatencyMS: 300, CreatedAt: old - int64(i)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RecordResult(&CheckResult{MonitorID: m.ID, OK: true, LatencyMS: 10, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}

	n, err := s.AggregateResults(now - 30*86400)
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if n != 3 {
		t.Errorf("deleted = %d, want 3", n)
	}
	_, total, _ := s.Results(m.ID, 1, 10)
	if total != 1 {
		t.Errorf("remaining = %d, want 1", total)
	}
	stat, _ := s.Uptime(m.ID, old-4000)
	if stat.Total != 4 || stat.OK != 3 {
		t.Errorf("uptime after aggregation = %d/%d, want 3/4", stat.OK, stat.Total)
	}

	m2, err := s.DeleteSummaries(now - 400*86400)
	if err != nil || m2 != 0 {
		t.Errorf("delete summaries = %d err %v", m2, err)
	}
	if m2, err = s.DeleteSummaries(now); m2 != 1 || err != nil {
		t.Errorf("delete summaries at now = %d err %v", m2, err)
	}
}
