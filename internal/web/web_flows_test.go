package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tickwatch/tickwatch/internal/store"
)

func loginSession(t *testing.T, h http.Handler, s *store.DB) (string, string) {
	t.Helper()
	_, password := createUser(t, s)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/login", strings.NewReader(url.Values{
		"username": {"admin"}, "password": {password},
	}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(rec, req)
	var session string
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			session = c.Value
		}
	}
	if session == "" {
		t.Fatal("no session cookie")
	}
	sess, err := s.Session(store.HashToken(session))
	if err != nil {
		t.Fatal(err)
	}
	return session, sess.CSRFToken
}

func authedRequest(session string, path string, form url.Values) *http.Request {
	req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
	return req
}

func authedGet(session string, path string) *http.Request {
	req := httptest.NewRequest("GET", path, nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
	return req
}

func createTestMonitor(t *testing.T, s *store.DB) *store.Monitor {
	t.Helper()
	m := &store.Monitor{
		Type: store.TypeHTTP, Name: "page-test",
		Config:   []byte(`{"url":"https://example.com","method":"GET","expect_status":"200-299"}`),
		Interval: 60, Timeout: 10, FailureThreshold: 3, Cooldown: 300,
		Enabled: true, Status: store.StatusUp,
		CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix(), NextRunAt: time.Now().Unix(),
	}
	store.Normalize(m)
	if err := s.CreateMonitor(m); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordResult(&store.CheckResult{MonitorID: m.ID, OK: true, StatusCode: 200, LatencyMS: 42, CreatedAt: time.Now().Unix()}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenIncident(m.ID, time.Now().Unix()-3600, "connection refused"); err != nil {
		t.Fatal(err)
	}
	if err := s.CloseIncidents(m.ID, time.Now().Unix()-1800); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestPageRenders(t *testing.T) {
	h, s := newWeb(t)
	session, _ := loginSession(t, h, s)
	m := createTestMonitor(t, s)

	pages := []struct {
		path     string
		contains string
	}{
		{"/", "page-test"},
		{"/monitors/new", "Add monitor"},
		{"/monitors/" + strconv.FormatInt(m.ID, 10), "Response time"},
		{"/monitors/" + strconv.FormatInt(m.ID, 10) + "/edit", "Edit monitor"},
		{"/channels", "Add channel"},
	}
	for _, p := range pages {
		t.Run(p.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, authedGet(session, p.path))
			if rec.Code != 200 {
				t.Fatalf("status = %d", rec.Code)
			}
			if !strings.Contains(rec.Body.String(), p.contains) {
				t.Errorf("body missing %q", p.contains)
			}
		})
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedGet(session, "/monitors/999"))
	if rec.Code != 404 {
		t.Errorf("missing monitor detail = %d, want 404", rec.Code)
	}
}

func TestMonitorLifecycleViaForms(t *testing.T) {
	h, s := newWeb(t)
	session, csrf := loginSession(t, h, s)

	form := url.Values{
		"_csrf":         {csrf},
		"type":          {"tcp"},
		"name":          {"db"},
		"interval":      {"60"},
		"timeout":       {"10"},
		"tcp_host":      {"db.internal"},
		"tcp_port":      {"5432"},
		"enabled":       {"on"},
		"channels_sent": {"1"},
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedRequest(session, "/monitors", form))
	if rec.Code != 303 && rec.Code != 302 {
		t.Fatalf("create status = %d, body: %s", rec.Code, rec.Body.String())
	}
	monitors, _ := s.Monitors()
	if len(monitors) != 1 {
		t.Fatalf("monitors = %d, want 1", len(monitors))
	}
	m := monitors[0]
	if m.Type != store.TypeTCP {
		t.Errorf("type = %s", m.Type)
	}
	tc, err := store.ParseTCPConfig(m.Config)
	if err != nil || tc.Host != "db.internal" || tc.Port != 5432 {
		t.Errorf("tcp config = %s, err %v", m.Config, err)
	}

	id := strconv.FormatInt(m.ID, 10)

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, authedRequest(session, "/monitors/"+id+"/pause", url.Values{"_csrf": {csrf}}))
	if rec.Code != 303 && rec.Code != 302 {
		t.Errorf("pause status = %d", rec.Code)
	}
	got, _ := s.Monitor(m.ID)
	if got.Enabled {
		t.Error("monitor should be paused")
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, authedRequest(session, "/monitors/"+id+"/resume", url.Values{"_csrf": {csrf}}))
	got, _ = s.Monitor(m.ID)
	if !got.Enabled {
		t.Error("monitor should be resumed")
	}

	rec = httptest.NewRecorder()
	update := url.Values{
		"_csrf": {csrf}, "type": {"tcp"}, "name": {"db-renamed"},
		"interval": {"30"}, "tcp_host": {"db2.internal"}, "tcp_port": {"5432"},
		"enabled_sent": {"1"},
	}
	h.ServeHTTP(rec, authedRequest(session, "/monitors/"+id+"/update", update))
	got, _ = s.Monitor(m.ID)
	if got.Name != "db-renamed" || got.Interval != 30 {
		t.Errorf("update not applied: %+v", got)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, authedRequest(session, "/monitors/"+id+"/delete", url.Values{"_csrf": {csrf}}))
	monitors, _ = s.Monitors()
	if len(monitors) != 0 {
		t.Errorf("monitors after delete = %d", len(monitors))
	}

	rec = httptest.NewRecorder()
	badForm := url.Values{"_csrf": {csrf}, "type": {"http"}, "name": {"x"}, "interval": {"5"}, "url": {"https://a.com"}}
	h.ServeHTTP(rec, authedRequest(session, "/monitors", badForm))
	if rec.Code != 400 {
		t.Errorf("invalid form status = %d, want 400", rec.Code)
	}
}

func TestChannelForms(t *testing.T) {
	h, s := newWeb(t)
	session, csrf := loginSession(t, h, s)

	form := url.Values{
		"_csrf":  {csrf},
		"type":   {"webhook"},
		"name":   {"ops"},
		"url":    {"https://example.com/hook"},
		"secret": {"s3cret"},
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedRequest(session, "/channels", form))
	if rec.Code != 303 && rec.Code != 302 {
		t.Fatalf("channel create status = %d", rec.Code)
	}
	channels, _ := s.Channels()
	if len(channels) != 1 {
		t.Fatalf("channels = %d, want 1", len(channels))
	}
	raw, _ := s.Channel(channels[0].ID)
	if !strings.Contains(string(raw.Config), "s3cret") {
		t.Error("secret not stored")
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, authedGet(session, "/channels?edit=1"))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Edit channel") {
		t.Errorf("edit page = %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, authedRequest(session, "/channels/1/update", url.Values{
		"_csrf": {csrf}, "type": {"webhook"}, "name": {"ops2"},
		"url": {"https://example.com/hook2"}, "secret": {"*****"},
	}))
	if rec.Code != 303 && rec.Code != 302 {
		t.Errorf("channel update status = %d", rec.Code)
	}
	raw, _ = s.Channel(1)
	if !strings.Contains(string(raw.Config), "s3cret") {
		t.Error("secret should be preserved when masked value resubmitted")
	}
	if !strings.Contains(string(raw.Config), "hook2") {
		t.Error("url not updated")
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, authedRequest(session, "/channels/1/delete", url.Values{"_csrf": {csrf}}))
	channels, _ = s.Channels()
	if len(channels) != 0 {
		t.Errorf("channels after delete = %d", len(channels))
	}
}

func TestDashSeriesEndpoint(t *testing.T) {
	h, s := newWeb(t)
	session, _ := loginSession(t, h, s)
	m := createTestMonitor(t, s)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedGet(session, "/dash/monitors/"+strconv.FormatInt(m.ID, 10)+"/series?hours=24"))
	if rec.Code != 200 {
		t.Fatalf("series status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "points") {
		t.Errorf("series body = %s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, authedGet(session, "/dash/monitors/abc/series"))
	if rec.Code != 400 {
		t.Errorf("bad id = %d, want 400", rec.Code)
	}
}
