package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/chaggangamer/tickwatch/internal/alert"
	"github.com/chaggangamer/tickwatch/internal/incident"
	"github.com/chaggangamer/tickwatch/internal/scheduler"
	"github.com/chaggangamer/tickwatch/internal/store"
	"github.com/go-chi/chi/v5"
)

type Deps struct {
	Store   store.Store
	Alerts  *alert.Dispatcher
	Sched   *scheduler.Scheduler
	Tracker *incident.Tracker
	BaseURL string
	Version string
	Log     *slog.Logger
}

func New(d Deps) http.Handler {
	if d.Log == nil {
		d.Log = slog.Default()
	}
	r := chi.NewRouter()
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusNotFound, "not_found", "the requested resource does not exist")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "this method is not supported on the route")
	})

	r.Get("/healthz", healthz(d))
	r.Get("/ping/{token}", ping(d))
	r.Post("/ping/{token}", ping(d))

	r.Route("/api/v1", func(api chi.Router) {
		api.Use(tokenAuth(d))
		api.Get("/monitors", listMonitors(d))
		api.Post("/monitors", createMonitor(d))
		api.Get("/monitors/{id}", getMonitor(d))
		api.Put("/monitors/{id}", updateMonitor(d))
		api.Delete("/monitors/{id}", deleteMonitor(d))
		api.Get("/monitors/{id}/results", listResults(d))
		api.Get("/monitors/{id}/incidents", listMonitorIncidents(d))
		api.Get("/incidents", listIncidents(d))
		api.Get("/channels", listChannels(d))
		api.Post("/channels", createChannel(d))
		api.Get("/channels/{id}", getChannel(d))
		api.Put("/channels/{id}", updateChannel(d))
		api.Delete("/channels/{id}", deleteChannel(d))
		api.Post("/channels/{id}/test", testChannel(d))
	})
	return r
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("encode response failed", "err", err)
	}
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{"code": code, "message": msg},
	})
}

func tokenAuth(d Deps) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			auth := r.Header.Get("Authorization")
			const prefix = "Bearer "
			if !strings.HasPrefix(auth, prefix) {
				writeErr(w, http.StatusUnauthorized, "unauthorized", "missing bearer token")
				return
			}
			hash := store.HashToken(strings.TrimPrefix(auth, prefix))
			if _, err := d.Store.APITokenByHash(hash); err != nil {
				writeErr(w, http.StatusUnauthorized, "unauthorized", "invalid token")
				return
			}
			_ = d.Store.TouchAPIToken(hash, time.Now().Unix())
			next.ServeHTTP(w, r)
		})
	}
}

func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id < 1 {
		return 0, false
	}
	return id, true
}

func pageParams(r *http.Request) (page, perPage int) {
	page, _ = strconv.Atoi(r.URL.Query().Get("page"))
	perPage, _ = strconv.Atoi(r.URL.Query().Get("per_page"))
	if page < 1 {
		page = 1
	}
	if perPage < 1 || perPage > 100 {
		perPage = 50
	}
	return page, perPage
}
