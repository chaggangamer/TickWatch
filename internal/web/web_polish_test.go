package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
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
		Username: "admin", PasswordHash: string(hash),
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
	h, s := newWebServer(t)
	createBootstrapUser(t, s)
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
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte("new-password-1")) != nil {
		t.Error("new hash not stored")
	}

	if _, newCsrf := loginAs(t, h, s, username, "new-password-1"); newCsrf == "" {
		t.Error("login with new password failed")
	}
}

func TestNotFoundPage(t *testing.T) {
	h, s := newWebServer(t)
	createBootstrapUser(t, s)
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
	h, s := newWebServer(t)
	createBootstrapUser(t, s)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/login", nil))
	if !strings.Contains(rec.Body.String(), "Tickwatch test") {
		t.Errorf("version missing from footer: %s", rec.Body.String())
	}
}

func setupTokens(t *testing.T, h http.Handler) (string, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/setup", nil))
	if rec.Code != 200 {
		t.Fatalf("setup page = %d", rec.Code)
	}
	var cookie string
	for _, c := range rec.Result().Cookies() {
		if c.Name == setupCookie {
			cookie = c.Value
		}
	}
	token := ""
	if m := regexp.MustCompile(`name="setup_token" value="([a-f0-9]+)"`).FindStringSubmatch(rec.Body.String()); m != nil {
		token = m[1]
	}
	if cookie == "" || token == "" {
		t.Fatal("setup token or cookie missing")
	}
	return cookie, token
}

func submitSetup(t *testing.T, h http.Handler, cookie, token, username, password, confirm string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/setup", strings.NewReader(url.Values{
		"setup_token":      {token},
		"username":         {username},
		"password":         {password},
		"confirm_password": {confirm},
	}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: setupCookie, Value: cookie})
	h.ServeHTTP(rec, req)
	return rec
}

func TestSetupRendersWhenNoUser(t *testing.T) {
	h, _ := newWebServer(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/setup", nil))
	if rec.Code != 200 {
		t.Fatalf("setup page = %d", rec.Code)
	}
	for _, want := range []string{"Create the first admin account", `name="username"`, `name="password"`, `name="confirm_password"`, `name="setup_token"`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("setup page missing %q", want)
		}
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 303 && rec.Code != 302 {
		t.Errorf("dashboard without user = %d, want redirect", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/setup" {
		t.Errorf("redirect target = %q, want /setup", loc)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/login", nil))
	if rec.Code != 303 && rec.Code != 302 {
		t.Errorf("login without user = %d, want redirect to setup", rec.Code)
	}
}

func TestSetupCreatesUserAndLogsIn(t *testing.T) {
	h, s := newWebServer(t)
	cookie, token := setupTokens(t, h)

	rec := submitSetup(t, h, cookie, token, "admin", "setup-password-1", "setup-password-1")
	if rec.Code != 303 && rec.Code != 302 {
		t.Fatalf("setup status = %d, body: %s", rec.Code, rec.Body.String())
	}
	var session string
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			session = c.Value
		}
	}
	if session == "" {
		t.Fatal("no session cookie after setup")
	}
	user, err := s.UserByName("admin")
	if err != nil {
		t.Fatal(err)
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte("setup-password-1")) != nil {
		t.Error("stored hash does not match password")
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/setup", nil))
	if rec.Code != 404 {
		t.Errorf("setup after first user = %d, want 404", rec.Code)
	}

	rec = httptest.NewRecorder()
	get := httptest.NewRequest("GET", "/", nil)
	get.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
	h.ServeHTTP(rec, get)
	if rec.Code != 200 {
		t.Errorf("dashboard after setup login = %d, want 200", rec.Code)
	}
}

func TestSetupRejectsBadInput(t *testing.T) {
	h, _ := newWebServer(t)
	cookie, token := setupTokens(t, h)

	cases := []struct {
		name                  string
		username, pw, confirm string
		want                  string
	}{
		{"short password", "admin", "short", "short", "at least 8 characters"},
		{"mismatch", "admin", "long-enough-1", "different-1", "Passwords do not match"},
		{"bad token", "admin", "long-enough-1", "long-enough-1", "Invalid or expired form token"},
		{"space in username", "two words", "long-enough-1", "long-enough-1", "without spaces"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			token := token
			if tc.name == "bad token" {
				token = "wrong-token"
			}
			rec := submitSetup(t, h, cookie, token, tc.username, tc.pw, tc.confirm)
			if rec.Code != 400 {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
			if !strings.Contains(rec.Body.String(), tc.want) {
				t.Errorf("body missing %q: %s", tc.want, rec.Body.String())
			}
		})
	}
}

func TestSetupConcurrentCreatesOneUser(t *testing.T) {
	h, s := newWebServer(t)

	const n = 8
	var wg sync.WaitGroup
	codes := make([]int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cookie, token := setupTokens(t, h)
			rec := submitSetup(t, h, cookie, token, "admin", "setup-password-1", "setup-password-1")
			codes[i] = rec.Code
		}(i)
	}
	wg.Wait()

	count, err := s.UserCount()
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("users = %d, want 1 after %d concurrent setups", count, n)
	}
	successes := 0
	for _, c := range codes {
		if c == 303 || c == 302 {
			successes++
		}
	}
	if successes != 1 {
		t.Errorf("successful setups = %d, want 1 (codes %v)", successes, codes)
	}
}

func TestChannelTestResponseDoesNotLeakSecret(t *testing.T) {
	const secret = "SECRETTOKEN123"
	h, s := newWebServer(t)
	createUser(t, s)
	session, csrf := loginAs(t, h, s, "admin", "hunter2")
	if err := s.CreateChannel(&store.Channel{Type: "webhook", Name: "ops", Config: []byte(`{"url":"http://127.0.0.1:1/` + secret + `"}`), CreatedAt: time.Now().Unix()}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedRequest(session, "/channels/1/test", url.Values{"_csrf": {csrf}}))
	if strings.Contains(rec.Body.String(), secret) {
		t.Fatalf("web response leaked secret: %s", rec.Body.String())
	}
}

func TestPauseResumeDownMonitorRecoversAndAlerts(t *testing.T) {
	var recoveryBodies []string
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		recoveryBodies = append(recoveryBodies, string(body))
		w.WriteHeader(http.StatusOK)
	}))
	defer hook.Close()

	h, s := newWebServer(t)
	username, password := createUser(t, s)
	session, csrf := loginAs(t, h, s, username, password)
	m := &store.Monitor{
		Type: store.TypeHTTP, Name: "down-http", Config: []byte(`{"url":"https://example.com"}`),
		Interval: 60, Timeout: 10, FailureThreshold: 1, Cooldown: 0, Enabled: true,
		Status: store.StatusDown, CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix(),
	}
	store.Normalize(m)
	if err := s.CreateMonitor(m); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenIncident(m.ID, time.Now().Unix()-30, "refused"); err != nil {
		t.Fatal(err)
	}
	ch := &store.Channel{Type: "webhook", Name: "recovery", Config: []byte(`{"url":"` + hook.URL + `"}`), CreatedAt: time.Now().Unix()}
	if err := s.CreateChannel(ch); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMonitorChannels(m.ID, []int64{ch.ID}); err != nil {
		t.Fatal(err)
	}

	id := strconv.FormatInt(m.ID, 10)
	for _, action := range []string{"pause", "resume"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, authedRequest(session, "/monitors/"+id+"/"+action, url.Values{"_csrf": {csrf}}))
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("%s status = %d", action, rec.Code)
		}
	}
	loaded, _ := s.Monitor(m.ID)
	tr := incident.New(s, slog.New(slog.NewTextHandler(io.Discard, nil)))
	tr.Load([]store.Monitor{*loaded}, mustOpenIncidents(s))
	recovery := tr.Observe(loaded, true, "", time.Now().Unix())
	if recovery == nil || recovery.Kind != incident.EventRecovered {
		t.Fatalf("recovery = %+v", recovery)
	}
	alerts := alert.New(s, slog.New(slog.NewTextHandler(io.Discard, nil)))
	alerts.Start(context.Background())
	alerts.NotifyRecovered(*loaded, recovery.Downtime)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(recoveryBodies) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if len(recoveryBodies) == 0 {
		t.Fatal("recovery alert was not sent")
	}
	if open, _ := s.OpenIncidents(); len(open) != 0 {
		t.Errorf("open incidents = %d", len(open))
	}
}

func TestEditDownHeartbeatRecovers(t *testing.T) {
	h, s := newWebServer(t)
	username, password := createUser(t, s)
	session, csrf := loginAs(t, h, s, username, password)
	m := &store.Monitor{
		Type: store.TypeHeartbeat, Name: "down-heartbeat", Config: []byte(`{"period":60,"grace":10}`),
		Interval: 60, Timeout: 10, FailureThreshold: 1, Cooldown: 0, Enabled: true,
		Status: store.StatusDown, CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix(),
	}
	store.Normalize(m)
	if err := s.CreateMonitor(m); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenIncident(m.ID, time.Now().Unix()-30, "missed heartbeat"); err != nil {
		t.Fatal(err)
	}

	id := strconv.FormatInt(m.ID, 10)
	form := url.Values{
		"_csrf": {csrf}, "enabled_sent": {"1"}, "type": {"heartbeat"}, "name": {m.Name},
		"interval": {"60"}, "timeout": {"10"}, "retries": {"0"}, "failure_threshold": {"1"},
		"cooldown": {"0"}, "period": {"60"}, "grace": {"10"}, "enabled": {"on"},
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedRequest(session, "/monitors/"+id+"/update", form))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("edit status = %d", rec.Code)
	}
	loaded, _ := s.Monitor(m.ID)
	tr := incident.New(s, slog.New(slog.NewTextHandler(io.Discard, nil)))
	tr.Load([]store.Monitor{*loaded}, mustOpenIncidents(s))
	if ev := tr.ForceRecover(loaded, time.Now().Unix()); ev == nil || ev.Kind != incident.EventRecovered {
		t.Fatalf("heartbeat recovery = %+v", ev)
	}
}

func mustOpenIncidents(s store.Store) []store.Incident {
	items, err := s.OpenIncidents()
	if err != nil {
		panic(err)
	}
	return items
}
