package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tickwatch/tickwatch/internal/alert"
	"github.com/tickwatch/tickwatch/internal/incident"
	"github.com/tickwatch/tickwatch/internal/scheduler"
	"github.com/tickwatch/tickwatch/internal/store"
	"golang.org/x/crypto/bcrypt"
)

func newWeb(t *testing.T) (http.Handler, *store.DB) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	al := alert.New(s, log)
	tr := incident.New(s, log)
	sched := scheduler.New(scheduler.Deps{Store: s, Tracker: tr, Alerts: al, Workers: 2, Log: log})
	h, err := New(Deps{
		Store: s, Sched: sched, Alerts: al, Tracker: tr, Log: log,
	})
	if err != nil {
		t.Fatal(err)
	}
	return h, s.(*store.DB)
}

func createUser(t *testing.T, s *store.DB) (string, string) {
	t.Helper()
	hash, _ := bcrypt.GenerateFromPassword([]byte("hunter2"), bcrypt.MinCost)
	if err := s.CreateUser(&store.User{Username: "admin", PasswordHash: string(hash), CreatedAt: time.Now().Unix()}); err != nil {
		t.Fatal(err)
	}
	return "admin", "hunter2"
}

func TestLoginRequired(t *testing.T) {
	h, _ := newWeb(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 303 && rec.Code != 302 {
		t.Errorf("dashboard without session = %d, want redirect", rec.Code)
	}
}

func TestLoginFlow(t *testing.T) {
	h, s := newWeb(t)
	username, password := createUser(t, s)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/login", strings.NewReader(url.Values{
		"username": {username}, "password": {"wrong"},
	}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Invalid username or password") {
		t.Errorf("wrong password: code=%d", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/login", strings.NewReader(url.Values{
		"username": {username}, "password": {password},
	}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(rec, req)
	if rec.Code != 303 && rec.Code != 302 {
		t.Fatalf("good login status = %d", rec.Code)
	}
	var session string
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			session = c.Value
		}
	}
	if session == "" {
		t.Fatal("no session cookie set")
	}
	sess, err := s.Session(store.HashToken(session))
	if err != nil {
		t.Fatalf("session persisted: %v", err)
	}
	if sess.CSRFToken == "" {
		t.Fatal("no csrf token on session")
	}

	rec = httptest.NewRecorder()
	get := httptest.NewRequest("GET", "/", nil)
	get.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
	h.ServeHTTP(rec, get)
	if rec.Code != 200 {
		t.Errorf("dashboard after login = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Add monitor") {
		t.Error("dashboard content missing")
	}
}

func TestCSRFRejected(t *testing.T) {
	h, s := newWeb(t)
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

	rec = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/monitors", strings.NewReader(url.Values{"name": {"x"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
	h.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Errorf("post without csrf = %d, want 400", rec.Code)
	}
}

func TestMonitorCreateFlow(t *testing.T) {
	h, s := newWeb(t)
	_, password := createUser(t, s)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/login", strings.NewReader(url.Values{
		"username": {"admin"}, "password": {password},
	}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(rec, req)
	var session, csrf string
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			session = c.Value
		}
	}
	sess, _ := s.Session(store.HashToken(session))
	csrf = sess.CSRFToken

	form := url.Values{
		"_csrf":     {csrf},
		"type":      {"http"},
		"name":      {"example"},
		"interval":  {"60"},
		"timeout":   {"10"},
		"url":       {"https://example.com"},
		"method":    {"GET"},
		"enabled":   {"on"},
	}
	req = httptest.NewRequest("POST", "/monitors", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 303 && rec.Code != 302 {
		t.Fatalf("create status = %d, body: %s", rec.Code, rec.Body.String())
	}
	monitors, _ := s.Monitors()
	if len(monitors) != 1 {
		t.Fatalf("monitors = %d, want 1", len(monitors))
	}
	if monitors[0].Name != "example" || monitors[0].Status != store.StatusUp {
		t.Errorf("monitor = %+v", monitors[0])
	}
}

func TestStatusPageShowsPublicOnly(t *testing.T) {
	h, s := newWeb(t)

	pub := &store.Monitor{
		Type: store.TypeHTTP, Name: "public-one",
		Config: []byte(`{"url":"https://example.com"}`),
		Interval: 60, Timeout: 10, FailureThreshold: 3, Cooldown: 300,
		Enabled: true, IsPublic: true, Status: store.StatusUp,
		CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix(), NextRunAt: time.Now().Unix(),
	}
	store.Normalize(pub)
	priv := &store.Monitor{
		Type: store.TypeHTTP, Name: "private-one",
		Config: []byte(`{"url":"https://example.com"}`),
		Interval: 60, Timeout: 10, FailureThreshold: 3, Cooldown: 300,
		Enabled: true, IsPublic: false, Status: store.StatusUp,
		CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix(), NextRunAt: time.Now().Unix(),
	}
	store.Normalize(priv)
	if err := s.CreateMonitor(pub); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateMonitor(priv); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/status", nil))
	if rec.Code != 200 {
		t.Fatalf("status page = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "public-one") {
		t.Error("public monitor missing from status page")
	}
	if strings.Contains(body, "private-one") {
		t.Error("private monitor leaked to status page")
	}
}

func TestDashStateRequiresSession(t *testing.T) {
	h, _ := newWeb(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/dash/state", nil))
	if rec.Code != 401 {
		t.Errorf("dash state without session = %d, want 401", rec.Code)
	}
}
