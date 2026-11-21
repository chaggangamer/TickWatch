package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/tickwatch/tickwatch/internal/store"
)

type monitorRequest struct {
	Type             string          `json:"type"`
	Name             string          `json:"name"`
	Config           json.RawMessage `json:"config"`
	Interval         int             `json:"interval_seconds"`
	Timeout          int             `json:"timeout_seconds"`
	Retries          int             `json:"retries"`
	FailureThreshold int             `json:"failure_threshold"`
	Cooldown         int             `json:"cooldown_seconds"`
	Enabled          *bool           `json:"enabled"`
	IsPublic         bool            `json:"is_public"`
	ChannelIDs       []int64         `json:"channel_ids"`
}

type monitorJSON struct {
	store.Monitor
	Config     json.RawMessage    `json:"config"`
	ChannelIDs []int64            `json:"channel_ids"`
	Uptime     map[string]float64 `json:"uptime,omitempty"`
	LatencyMS  int64              `json:"latency_ms,omitempty"`
	PingURL    string             `json:"ping_url,omitempty"`
}

type monitorStats struct {
	latest map[int64]store.CheckResult
	uptime map[int64]store.UptimeStat
}

func loadStats(d Deps) monitorStats {
	stats := monitorStats{}
	stats.latest, _ = d.Store.LatestResults()
	stats.uptime, _ = d.Store.UptimeBatch(time.Now().Unix() - 90*86400)
	return stats
}

func buildMonitor(d Deps, m store.Monitor, stats *monitorStats) monitorJSON {
	out := monitorJSON{Monitor: m, Config: json.RawMessage(m.Config)}
	if out.Config == nil {
		out.Config = json.RawMessage("{}")
	}
	channels, err := d.Store.MonitorChannels(m.ID)
	if err == nil {
		out.ChannelIDs = make([]int64, 0, len(channels))
		for _, c := range channels {
			out.ChannelIDs = append(out.ChannelIDs, c.ID)
		}
	}
	if m.Type == store.TypeHeartbeat && m.HeartbeatToken != "" && d.BaseURL != "" {
		out.PingURL = d.BaseURL + "/ping/" + m.HeartbeatToken
	}
	if stats == nil {
		loaded := loadStats(d)
		stats = &loaded
	}
	out.Uptime = map[string]float64{}
	now := time.Now().Unix()
	windows := []struct {
		name  string
		since int64
	}{
		{"24h", now - 86400},
		{"7d", now - 7*86400},
		{"30d", now - 30*86400},
		{"90d", now - 90*86400},
	}
	if stat, ok := stats.uptime[m.ID]; ok && stat.Total > 0 {
		out.Uptime["24h"] = round2(stat.Percent())
	}
	for _, win := range windows[1:] {
		if win.name == "90d" {
			if stat, ok := stats.uptime[m.ID]; ok && stat.Total > 0 {
				out.Uptime[win.name] = round2(stat.Percent())
			}
			continue
		}
		stat, err := d.Store.Uptime(m.ID, win.since)
		if err == nil && stat.Percent() >= 0 {
			out.Uptime[win.name] = round2(stat.Percent())
		}
	}
	if res, ok := stats.latest[m.ID]; ok && res.OK {
		out.LatencyMS = res.LatencyMS
	}
	return out
}

func round2(f float64) float64 {
	return float64(int(f*100+0.5)) / 100
}

func monitorFromRequest(req *monitorRequest, existing *store.Monitor) (*store.Monitor, error) {
	m := &store.Monitor{}
	if existing != nil {
		*m = *existing
	} else {
		m.Type = req.Type
	}
	m.Name = req.Name
	if req.Config != nil {
		m.Config = req.Config
	}
	m.Interval = req.Interval
	m.Timeout = req.Timeout
	m.Retries = req.Retries
	m.FailureThreshold = req.FailureThreshold
	m.Cooldown = req.Cooldown
	m.IsPublic = req.IsPublic
	if req.Enabled != nil {
		m.Enabled = *req.Enabled
	} else if existing == nil {
		m.Enabled = true
	}
	store.Normalize(m)
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return m, nil
}

func listMonitors(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		monitors, err := d.Store.Monitors()
		if err != nil {
			writeErr(w, 500, "internal", "list monitors failed")
			return
		}
		stats := loadStats(d)
		out := make([]monitorJSON, 0, len(monitors))
		for _, m := range monitors {
			out = append(out, buildMonitor(d, m, &stats))
		}
		writeJSON(w, 200, map[string]any{"items": out})
	}
}

func createMonitor(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req monitorRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "invalid_json", "request body is not valid json")
			return
		}
		m, err := monitorFromRequest(&req, nil)
		if err != nil {
			writeErr(w, 400, "invalid_monitor", err.Error())
			return
		}
		if m.Type == store.TypeHeartbeat {
			m.HeartbeatToken = store.NewToken(16)
		}
		if err := d.Store.CreateMonitor(m); err != nil {
			writeErr(w, 500, "internal", "create monitor failed")
			return
		}
		if req.ChannelIDs != nil {
			if err := d.Store.SetMonitorChannels(m.ID, req.ChannelIDs); err != nil {
				writeErr(w, 400, "invalid_channels", "one or more channel ids do not exist")
				return
			}
		}
		fresh, _ := d.Store.Monitor(m.ID)
		if fresh == nil {
			fresh = m
		}
		writeJSON(w, 201, buildMonitor(d, *fresh, nil))
	}
}

func getMonitor(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(r)
		if !ok {
			writeErr(w, 400, "invalid_id", "monitor id must be a positive integer")
			return
		}
		m, err := d.Store.Monitor(id)
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, 404, "not_found", "monitor not found")
			return
		}
		if err != nil {
			writeErr(w, 500, "internal", "get monitor failed")
			return
		}
		writeJSON(w, 200, buildMonitor(d, *m, nil))
	}
}

func updateMonitor(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(r)
		if !ok {
			writeErr(w, 400, "invalid_id", "monitor id must be a positive integer")
			return
		}
		existing, err := d.Store.Monitor(id)
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, 404, "not_found", "monitor not found")
			return
		}
		if err != nil {
			writeErr(w, 500, "internal", "get monitor failed")
			return
		}
		var req monitorRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "invalid_json", "request body is not valid json")
			return
		}
		m, err := monitorFromRequest(&req, existing)
		if err != nil {
			writeErr(w, 400, "invalid_monitor", err.Error())
			return
		}
		if m.Type == store.TypeHeartbeat && req.Config != nil {
			m.NextDeadlineAt = time.Now().Unix() + int64(m.HeartbeatPeriod()+m.HeartbeatGrace())
		}
		if err := d.Store.UpdateMonitor(m); err != nil {
			writeErr(w, 500, "internal", "update monitor failed")
			return
		}
		if req.ChannelIDs != nil {
			if err := d.Store.SetMonitorChannels(m.ID, req.ChannelIDs); err != nil {
				writeErr(w, 400, "invalid_channels", "one or more channel ids do not exist")
				return
			}
		}
		fresh, _ := d.Store.Monitor(id)
		if fresh == nil {
			fresh = m
		}
		writeJSON(w, 200, buildMonitor(d, *fresh, nil))
	}
}

func deleteMonitor(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(r)
		if !ok {
			writeErr(w, 400, "invalid_id", "monitor id must be a positive integer")
			return
		}
		err := d.Store.DeleteMonitor(id)
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, 404, "not_found", "monitor not found")
			return
		}
		if err != nil {
			writeErr(w, 500, "internal", "delete monitor failed")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
