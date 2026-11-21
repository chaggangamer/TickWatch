package web

import (
	"embed"
	"encoding/json"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/tickwatch/tickwatch/internal/alert"
	"github.com/tickwatch/tickwatch/internal/incident"
	"github.com/tickwatch/tickwatch/internal/scheduler"
	"github.com/tickwatch/tickwatch/internal/store"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/*
var staticFS embed.FS

const sessionCookie = "tickwatch_session"
const sessionTTL = 7 * 24 * time.Hour

type Deps struct {
	Store   store.Store
	Sched   *scheduler.Scheduler
	Alerts  *alert.Dispatcher
	Tracker *incident.Tracker
	Log     *slog.Logger
	Secure  bool
}

type server struct {
	store   store.Store
	sched   *scheduler.Scheduler
	alerts  *alert.Dispatcher
	tracker *incident.Tracker
	log     *slog.Logger
	secure  bool
	tpl     map[string]*template.Template
	limiter *loginLimiter
}

func New(d Deps) (http.Handler, error) {
	if d.Log == nil {
		d.Log = slog.Default()
	}
	s := &server{
		store:   d.Store,
		sched:   d.Sched,
		alerts:  d.Alerts,
		tracker: d.Tracker,
		log:     d.Log,
		secure:  d.Secure,
		limiter: newLoginLimiter(),
	}
	if err := s.parseTemplates(); err != nil {
		return nil, err
	}

	mux := chi.NewRouter()
	mux.Handle("/static/*", staticHandler())
	mux.Get("/login", s.loginPage)
	mux.Post("/login", s.loginSubmit)
	mux.Get("/status", s.statusPage)

	mux.Group(func(priv chi.Router) {
		priv.Use(s.requireSession)
		priv.Get("/", s.dashboard)
		priv.Get("/logout", s.logout)
		priv.Get("/monitors/new", s.monitorForm)
		priv.Get("/monitors/{id}", s.monitorDetail)
		priv.Get("/monitors/{id}/edit", s.monitorForm)
		priv.Post("/monitors", s.monitorCreate)
		priv.Post("/monitors/{id}/update", s.monitorUpdate)
		priv.Post("/monitors/{id}/delete", s.monitorDelete)
		priv.Post("/monitors/{id}/pause", s.monitorPause)
		priv.Post("/monitors/{id}/resume", s.monitorResume)
		priv.Get("/channels", s.channelsPage)
		priv.Post("/channels", s.channelCreate)
		priv.Post("/channels/{id}/update", s.channelUpdate)
		priv.Post("/channels/{id}/delete", s.channelDelete)
		priv.Post("/channels/{id}/test", s.channelTest)
		priv.Get("/dash/state", s.dashState)
		priv.Get("/dash/monitors/{id}/series", s.dashSeries)
	})
	return mux, nil
}

func staticHandler() http.HandlerFunc {
	fs := http.FS(staticFS)
	fileServer := http.StripPrefix("/static/", http.FileServer(fs))
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		fileServer.ServeHTTP(w, r)
	}
}

type sessionCtx struct {
	userID int64
	csrf   string
}

func (s *server) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil || c.Value == "" {
			s.reject(w, r)
			return
		}
		sess, err := s.store.Session(store.HashToken(c.Value))
		if err != nil {
			s.reject(w, r)
			return
		}
		if sess.ExpiresAt < time.Now().Unix() {
			_ = s.store.DeleteSession(sess.TokenHash)
			s.reject(w, r)
			return
		}
		ctx := withSession(r.Context(), sessionCtx{userID: sess.UserID, csrf: sess.CSRFToken})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *server) reject(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/dash/") {
		writeWErr(w, 401, "unauthorized", "login required")
		return
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *server) csrfCheck(r *http.Request) bool {
	sess := sessionFrom(r.Context())
	if sess == nil {
		return false
	}
	token := r.FormValue("_csrf")
	if token == "" {
		token = r.Header.Get("X-CSRF-Token")
	}
	return constantTimeEqual(token, sess.csrf)
}

func (s *server) createSession(w http.ResponseWriter, userID int64) error {
	raw := store.NewToken(32)
	sess := &store.Session{
		TokenHash: store.HashToken(raw),
		UserID:    userID,
		CSRFToken: store.NewToken(16),
		ExpiresAt: time.Now().Add(sessionTTL).Unix(),
	}
	if err := s.store.CreateSession(sess); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    raw,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
	return nil
}

func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	if !s.csrfCheck(r) {
		writeWErr(w, 403, "csrf", "invalid csrf token")
		return
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		_ = s.store.DeleteSession(store.HashToken(c.Value))
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

type loginLimiter struct {
	mu       sync.Mutex
	attempts map[string]*attemptWindow
}

type attemptWindow struct {
	count int
	start time.Time
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{attempts: map[string]*attemptWindow{}}
}

func (l *loginLimiter) blocked(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	w, ok := l.attempts[ip]
	if !ok {
		return false
	}
	if time.Since(w.start) > time.Minute {
		delete(l.attempts, ip)
		return false
	}
	return w.count >= 5
}

func (l *loginLimiter) record(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	w, ok := l.attempts[ip]
	if !ok || time.Since(w.start) > time.Minute {
		l.attempts[ip] = &attemptWindow{count: 1, start: time.Now()}
		return
	}
	w.count++
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func writeWErr(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{"code": code, "message": msg},
	})
}

func constantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := 0; i < len(a); i++ {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

var _ = chi.URLParam
