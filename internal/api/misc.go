package api

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/tickwatch/tickwatch/internal/store"
)

func healthz(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := d.Store.Ping(); err != nil {
			writeErr(w, 503, "unavailable", "database is not reachable")
			return
		}
		writeJSON(w, 200, map[string]any{"status": "ok", "version": d.Version})
	}
}

func ping(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := chi.URLParam(r, "token")
		if token == "" {
			writeErr(w, 400, "invalid_token", "missing ping token")
			return
		}
		if err := d.Sched.HandlePing(token); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeErr(w, 404, "not_found", "unknown ping token")
				return
			}
			writeErr(w, 500, "internal", "heartbeat processing failed")
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(200)
		_, _ = w.Write([]byte("OK\n"))
	}
}

func listResults(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(r)
		if !ok {
			writeErr(w, 400, "invalid_id", "monitor id must be a positive integer")
			return
		}
		if _, err := d.Store.Monitor(id); errors.Is(err, store.ErrNotFound) {
			writeErr(w, 404, "not_found", "monitor not found")
			return
		}
		page, perPage := pageParams(r)
		items, total, err := d.Store.Results(id, page, perPage)
		if err != nil {
			writeErr(w, 500, "internal", "list results failed")
			return
		}
		if items == nil {
			items = []store.CheckResult{}
		}
		writeJSON(w, 200, map[string]any{
			"items": items, "page": page, "per_page": perPage, "total": total,
		})
	}
}

func listMonitorIncidents(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(r)
		if !ok {
			writeErr(w, 400, "invalid_id", "monitor id must be a positive integer")
			return
		}
		if _, err := d.Store.Monitor(id); errors.Is(err, store.ErrNotFound) {
			writeErr(w, 404, "not_found", "monitor not found")
			return
		}
		page, perPage := pageParams(r)
		items, total, err := d.Store.Incidents(id, page, perPage)
		if err != nil {
			writeErr(w, 500, "internal", "list incidents failed")
			return
		}
		if items == nil {
			items = []store.Incident{}
		}
		writeJSON(w, 200, map[string]any{
			"items": items, "page": page, "per_page": perPage, "total": total,
		})
	}
}

func listIncidents(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		page, perPage := pageParams(r)
		items, total, err := d.Store.AllIncidents(page, perPage)
		if err != nil {
			writeErr(w, 500, "internal", "list incidents failed")
			return
		}
		if items == nil {
			items = []store.Incident{}
		}
		writeJSON(w, 200, map[string]any{
			"items": items, "page": page, "per_page": perPage, "total": total,
		})
	}
}
