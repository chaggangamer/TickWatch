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

	"github.com/chaggangamer/tickwatch/internal/alert"
	"github.com/chaggangamer/tickwatch/internal/incident"
	"github.com/chaggangamer/tickwatch/internal/scheduler"
	"github.com/chaggangamer/tickwatch/internal/store"
	"golang.org/x/crypto/bcrypt"
)

func newWebServer(t *testing.T) (http.Handler, *store.DB) {
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
		Store: s, Sched: sched, Alerts: al, Tracker: tr, Log: log, Version: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	return h, s.(*store.DB)
}

func createBootstrapUser(t *testing.T, s *store.DB) (string, string) {
	t.Helper()
	hash, _ := bcrypt.GenerateFromPassword([]byte("bootstrap-pw"), bcrypt.MinCost)
	if err := s.CreateUser(&store.User{
		Username: "admin", PasswordHash: string(hash), PasswordGenerated: true,
		CreatedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	return "admin", "bootstrap-pw"
}

func loginAs(t *testing.T, h http.Handler, s *store.DB, username, password string) (string, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/login", strings.NewReader(url.Values{
		"username": {username}, "password": {password},
	}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(rec, req)
	if rec.Code != 303 && rec.Code != 302 {
		t.Fatalf("login status = %d", rec.Code)
	}
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

func TestLoginRenders(t *testing.T) {
	h, _ := newWebServer(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/login", nil))
	if rec.Code != 200 {
		t.Fatalf("login status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"Tickwatch", "Sign in to your dashboard", "name=\"username\"", "name=\"password\"", "/static/style.css"} {
		if !strings.Contains(body, want) {
			t.Errorf("login page missing %q", want)
		}
	}
}

func TestChangePasswordFlow(t *testing.T) {
	h, s := newWebServer(t)
	username, password := createBootstrapUser(t, s)
	session, csrf := loginAs(t, h, s, username, password)

	rec := httptest.NewRecorder()
	get := httptest.NewRequest("GET", "/", nil)
	get.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
	h.ServeHTTP(rec, get)
	if !strings.Contains(rec.Body.String(), "bootstrap password") {
		t.Error("first run notice missing on dashboard")
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, authedGet(session, "/password"))
	if rec.Code != 200 {
		t.Fatalf("password page = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Change password") {
		t.Error("password page content missing")
	}

	post := func(form url.Values) string {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, authedRequest(session, "/password", form))
		return rec.Body.String()
	}

	body := post(url.Values{
		"_csrf": {csrf}, "current_password": {"wrong"}, "new_password": {"new-password-1"}, "confirm_password": {"new-password-1"},
	})
	if !strings.Contains(body, "Current password is incorrect") {
		t.Errorf("wrong current accepted: %s", body)
	}

	body = post(url.Values{
		"_csrf": {csrf}, "current_password": {password}, "new_password": {"new-password-1"}, "confirm_password": {"different"},
	})
	if !strings.Contains(body, "Passwords do not match") {
		t.Errorf("mismatch accepted: %s", body)
	}

	body = post(url.Values{
		"_csrf": {csrf}, "current_password": {password}, "new_password": {"short"}, "confirm_password": {"short"},
	})
	if !strings.Contains(body, "at least 8 characters") {
		t.Errorf("short password accepted: %s", body)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, authedRequest(session, "/password", url.Values{
		"_csrf": {csrf}, "current_password": {password}, "new_password": {"new-password-1"}, "confirm_password": {"new-password-1"},
	}))
	if rec.Code != 303 && rec.Code != 302 {
		t.Fatalf("successful change status = %d, body %s", rec.Code, rec.Body.String())
	}

	user, err := s.UserByName(username)
	if err != nil {
		t.Fatal(err)
	}
	if user.PasswordGenerated {
		t.Error("bootstrap flag should be cleared")
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte("new-password-1")) != nil {
		t.Error("new hash not stored")
	}

	if _, newCsrf := loginAs(t, h, s, username, "new-password-1"); newCsrf == "" {
		t.Error("login with new password failed")
	}

	rec = httptest.NewRecorder()
	get2 := httptest.NewRequest("GET", "/", nil)
	get2.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
	h.ServeHTTP(rec, get2)
	if strings.Contains(rec.Body.String(), "bootstrap password") {
		t.Error("notice should disappear after password change")
	}
}

func TestNotFoundPage(t *testing.T) {
	h, _ := newWebServer(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/definitely-not-here", nil))
	if rec.Code != 404 {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"Page not found", "Back to dashboard", "/static/style.css"} {
		if !strings.Contains(body, want) {
			t.Errorf("404 page missing %q", want)
		}
	}
}

func TestVersionInFooter(t *testing.T) {
	h, _ := newWebServer(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/login", nil))
	if !strings.Contains(rec.Body.String(), "Tickwatch test") {
		t.Errorf("version missing from footer: %s", rec.Body.String())
	}
}
