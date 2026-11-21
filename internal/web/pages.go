package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/chaggangamer/tickwatch/internal/store"
	"github.com/go-chi/chi/v5"
	"golang.org/x/crypto/bcrypt"
)

func (s *server) loginPage(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		if sess, err := s.store.Session(store.HashToken(c.Value)); err == nil && sess.ExpiresAt > time.Now().Unix() {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
	}
	s.render(w, r, 200, "login", map[string]any{"Error": ""})
}

func (s *server) loginSubmit(w http.ResponseWriter, r *http.Request) {
	if s.limiter.blocked(clientIP(r)) {
		s.render(w, r, 429, "login", map[string]any{"Error": "Too many attempts. Try again in a minute."})
		return
	}
	username := r.FormValue("username")
	password := r.FormValue("password")
	user, err := s.store.UserByName(username)
	if err != nil {
		s.limiter.record(clientIP(r))
		s.render(w, r, 200, "login", map[string]any{"Error": "Invalid username or password."})
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) != nil {
		s.limiter.record(clientIP(r))
		s.render(w, r, 200, "login", map[string]any{"Error": "Invalid username or password."})
		return
	}
	if err := s.createSession(w, user.ID); err != nil {
		s.render(w, r, 500, "login", map[string]any{"Error": "Could not create session."})
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

type monitorRow struct {
	store.Monitor
	LatencyMS int64
	Uptime24  float64
	HasUptime bool
	Spark     []int64
	Target    string
}

func (s *server) dashboard(w http.ResponseWriter, r *http.Request) {
	monitors, err := s.store.Monitors()
	if err != nil {
		s.errorPage(w, r, http.StatusInternalServerError, "Something went wrong", "Could not load monitors. Check the server logs.")
		return
	}
	sess := sessionFrom(r.Context())
	passwordNotice := false
	if user, err := s.store.UserByID(sess.userID); err == nil {
		passwordNotice = user.PasswordGenerated
	}
	rows := make([]monitorRow, 0, len(monitors))
	latest, _ := s.store.LatestResults()
	uptime, _ := s.store.UptimeBatch(time.Now().Unix() - 86400)
	for _, m := range monitors {
		row := monitorRow{Monitor: m, Target: m.Target()}
		if res, ok := latest[m.ID]; ok && res.OK {
			row.LatencyMS = res.LatencyMS
		}
		if stat, ok := uptime[m.ID]; ok && stat.Total > 0 {
			row.Uptime24 = stat.Percent()
			row.HasUptime = true
		}
		recent, _ := s.store.RecentResults(m.ID, 30)
		for i := len(recent) - 1; i >= 0; i-- {
			if recent[i].OK {
				row.Spark = append(row.Spark, recent[i].LatencyMS)
			}
		}
		rows = append(rows, row)
	}
	s.render(w, r, 200, "monitors", map[string]any{
		"Meta":           pageMeta{CSRF: sess.csrf, Title: "Monitors"},
		"Monitors":       rows,
		"PasswordNotice": passwordNotice,
	})
}

func (s *server) monitorDetail(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	m, err := s.store.Monitor(id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.errorPage(w, r, http.StatusInternalServerError, "Something went wrong", "Could not load the monitor. Check the server logs.")
		return
	}
	results, _, _ := s.store.Results(m.ID, 1, 50)
	incidents, _, _ := s.store.Incidents(m.ID, 1, 20)
	series, _ := s.store.Series(m.ID, time.Now().Unix()-86400, 300)
	channels, _ := s.store.MonitorChannels(m.ID)
	sess := sessionFrom(r.Context())

	now := time.Now().Unix()
	stat24, _ := s.store.Uptime(m.ID, now-86400)
	stat7, _ := s.store.Uptime(m.ID, now-7*86400)
	stat30, _ := s.store.Uptime(m.ID, now-30*86400)
	var avgMS int64
	var counted int64
	for _, res := range results {
		if res.OK && res.LatencyMS > 0 {
			avgMS += res.LatencyMS
			counted++
		}
	}
	if counted > 0 {
		avgMS /= counted
	}

	var pingURL string
	if m.Type == store.TypeHeartbeat && m.HeartbeatToken != "" {
		pingURL = "/ping/" + m.HeartbeatToken
	}
	var sslDays int64
	if m.SSLExpiresAt > 0 {
		sslDays = (m.SSLExpiresAt - now) / 86400
	}
	s.render(w, r, 200, "monitor_detail", map[string]any{
		"Meta":      pageMeta{CSRF: sess.csrf, Title: m.Name},
		"Monitor":   m,
		"Target":    m.Target(),
		"PingURL":   pingURL,
		"Results":   results,
		"Incidents": incidents,
		"Series":    series,
		"Channels":  channels,
		"SSLDays":   sslDays,
		"Uptime24":  percentOrZero(stat24),
		"Uptime7":   percentOrZero(stat7),
		"Uptime30":  percentOrZero(stat30),
		"AvgMS":     avgMS,
	})
}

func percentOrZero(stat store.UptimeStat) float64 {
	if stat.Total == 0 {
		return 0
	}
	return stat.Percent()
}

func (s *server) monitorForm(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	meta := pageMeta{CSRF: sess.csrf, Title: "Add monitor"}
	data := map[string]any{
		"Meta":     meta,
		"IsEdit":   false,
		"Monitor":  &store.Monitor{Interval: 60, Timeout: 10, FailureThreshold: 3, Cooldown: 300},
		"HTTP":     store.HTTPConfig{Method: "GET", ExpectStatus: "200-299"},
		"Channels": s.channelList(),
		"Selected": map[int64]bool{},
	}
	if idStr := chi.URLParam(r, "id"); idStr != "" {
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		m, err := s.store.Monitor(id)
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			s.errorPage(w, r, http.StatusInternalServerError, "Something went wrong", "Could not load the monitor. Check the server logs.")
			return
		}
		data["IsEdit"] = true
		data["Monitor"] = m
		data["Target"] = m.Target()
		switch m.Type {
		case store.TypeHTTP:
			if hc, err := store.ParseHTTPConfig(m.Config); err == nil {
				data["HTTP"] = hc
				data["HeaderLines"] = headerLines(hc.Headers)
			}
		case store.TypeTCP:
			if tc, err := store.ParseTCPConfig(m.Config); err == nil {
				data["TCP"] = tc
			}
		case store.TypePing:
			if pc, err := store.ParsePingConfig(m.Config); err == nil {
				data["Ping"] = pc
			}
		case store.TypeHeartbeat:
			if hb, err := store.ParseHeartbeatConfig(m.Config); err == nil {
				data["Heartbeat"] = hb
			}
			data["PingURL"] = "/ping/" + m.HeartbeatToken
		}
		linked, _ := s.store.MonitorChannels(m.ID)
		selected := map[int64]bool{}
		for _, c := range linked {
			selected[c.ID] = true
		}
		data["Selected"] = selected
		meta.Title = "Edit " + m.Name
		data["Meta"] = meta
	}
	s.render(w, r, 200, "monitor_form", data)
}

func headerLines(headers map[string]string) string {
	out := ""
	for k, v := range headers {
		out += k + ": " + v + "\n"
	}
	return out
}

func (s *server) channelList() []store.Channel {
	channels, _ := s.store.Channels()
	return channels
}

func (s *server) channelsPage(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	data := map[string]any{
		"Meta":        pageMeta{CSRF: sess.csrf, Title: "Alert channels"},
		"Channels":    s.channelsListForPage(),
		"EditChannel": nil,
	}
	if editID := r.URL.Query().Get("edit"); editID != "" {
		id, err := strconv.ParseInt(editID, 10, 64)
		if err == nil {
			c, err := s.store.Channel(id)
			if err == nil {
				data["EditChannel"] = c
				data["Cfg"] = cfgForForm(c)
			}
		}
	}
	s.render(w, r, 200, "channels", data)
}

func cfgForForm(c *store.Channel) map[string]any {
	var m map[string]any
	if err := json.Unmarshal(c.Config, &m); err != nil || m == nil {
		return map[string]any{}
	}
	out := map[string]any{}
	for k, v := range m {
		out[k] = v
	}
	if to, ok := m["to"].([]any); ok {
		var parts []string
		for _, item := range to {
			if s, ok := item.(string); ok {
				parts = append(parts, s)
			}
		}
		out["to_join"] = strings.Join(parts, ", ")
	}
	for _, key := range channelSecretKeys(c.Type) {
		if v, ok := out[key].(string); ok && v != "" {
			out[key] = "*****"
		}
	}
	return out
}

func (s *server) statusPage(w http.ResponseWriter, r *http.Request) {
	monitors, err := s.store.PublicMonitors()
	if err != nil {
		s.errorPage(w, r, http.StatusInternalServerError, "Something went wrong", "Could not load monitors. Check the server logs.")
		return
	}
	now := time.Now().Unix()
	latest, _ := s.store.LatestResults()
	rows := make([]map[string]any, 0, len(monitors))
	anyDown := false
	for _, m := range monitors {
		if m.Enabled && m.Status == store.StatusDown {
			anyDown = true
		}
		row := map[string]any{
			"Monitor": m,
			"Target":  m.Target(),
		}
		if res, ok := latest[m.ID]; ok && res.OK {
			row["LatencyMS"] = res.LatencyMS
		}
		for _, win := range []struct {
			name  string
			since int64
		}{{"u24", now - 86400}, {"u7", now - 7*86400}, {"u30", now - 30*86400}} {
			stat, _ := s.store.Uptime(m.ID, win.since)
			if stat.Total > 0 {
				row[win.name] = stat.Percent()
			} else {
				row[win.name] = nil
			}
		}
		row["Days"] = buildDayBars(s.store.DailyUptime(m.ID, 90, now))
		rows = append(rows, row)
	}
	s.render(w, r, 200, "status", map[string]any{
		"Meta":     pageMeta{Title: "Status"},
		"Monitors": rows,
		"AnyDown":  anyDown,
	})
}

func buildDayBars(days []store.DayStat, err error) []map[string]any {
	if err != nil || days == nil {
		days = []store.DayStat{}
	}
	out := make([]map[string]any, 0, 90)
	for i, d := range days {
		bar := map[string]any{"X": i * 4}
		switch {
		case d.Total == 0:
			bar["Class"] = "bar-none"
		case d.Percent() >= 99:
			bar["Class"] = "bar-ok"
		case d.Percent() >= 95:
			bar["Class"] = "bar-warn"
		default:
			bar["Class"] = "bar-fail"
		}
		out = append(out, bar)
	}
	return out
}

func (s *server) dashState(w http.ResponseWriter, r *http.Request) {
	monitors, err := s.store.Monitors()
	if err != nil {
		writeWErr(w, 500, "internal", "load monitors failed")
		return
	}
	latest, _ := s.store.LatestResults()
	uptime, _ := s.store.UptimeBatch(time.Now().Unix() - 86400)
	items := make([]map[string]any, 0, len(monitors))
	for _, m := range monitors {
		item := map[string]any{
			"id":      m.ID,
			"name":    m.Name,
			"type":    m.Type,
			"status":  m.Status,
			"enabled": m.Enabled,
		}
		if res, ok := latest[m.ID]; ok && res.OK {
			item["latency_ms"] = res.LatencyMS
		}
		if stat, ok := uptime[m.ID]; ok && stat.Total > 0 {
			item["uptime_24h"] = stat.Percent()
		}
		if m.Enabled {
			if m.Type == store.TypeHeartbeat {
				item["next_deadline_at"] = m.NextDeadlineAt
			} else {
				item["next_run_at"] = m.NextRunAt
			}
		}
		items = append(items, item)
	}
	writeJSONOK(w, map[string]any{"items": items, "time": time.Now().Unix()})
}

func (s *server) dashSeries(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeWErr(w, 400, "invalid_id", "monitor id must be a positive integer")
		return
	}
	hours := 24
	if h := r.URL.Query().Get("hours"); h != "" {
		if n, err := strconv.Atoi(h); err == nil && n >= 1 && n <= 720 {
			hours = n
		}
	}
	points, err := s.store.Series(id, time.Now().Unix()-int64(hours)*3600, 400)
	if err != nil {
		writeWErr(w, 500, "internal", "load series failed")
		return
	}
	if points == nil {
		points = []store.Point{}
	}
	writeJSONOK(w, map[string]any{"points": points})
}

func writeJSONOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (s *server) errorPage(w http.ResponseWriter, r *http.Request, status int, title, msg string) {
	sess := sessionFrom(r.Context())
	var csrf string
	if sess != nil {
		csrf = sess.csrf
	}
	s.render(w, r, status, "error", map[string]any{
		"Meta":    pageMeta{CSRF: csrf, Title: title},
		"Code":    status,
		"Title":   title,
		"Message": msg,
	})
}

func (s *server) passwordPage(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	user, err := s.store.UserByID(sess.userID)
	mustChange := err == nil && user.PasswordGenerated
	s.render(w, r, 200, "password", map[string]any{
		"Meta":       pageMeta{CSRF: sess.csrf, Title: "Change password"},
		"Changed":    r.URL.Query().Get("changed") == "1",
		"MustChange": mustChange,
		"Error":      "",
	})
}

func (s *server) passwordSubmit(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	renderErr := func(msg string) {
		s.render(w, r, 400, "password", map[string]any{
			"Meta":  pageMeta{CSRF: sess.csrf, Title: "Change password"},
			"Error": msg,
		})
	}
	if !s.csrfCheck(r) {
		renderErr("Invalid form token, please retry.")
		return
	}
	user, err := s.store.UserByID(sess.userID)
	if err != nil {
		renderErr("Could not load your account.")
		return
	}
	current := r.FormValue("current_password")
	next := r.FormValue("new_password")
	confirm := r.FormValue("confirm_password")
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(current)) != nil {
		renderErr("Current password is incorrect.")
		return
	}
	if len(next) < 8 {
		renderErr("New password must be at least 8 characters.")
		return
	}
	if next != confirm {
		renderErr("Passwords do not match.")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(next), bcrypt.DefaultCost)
	if err != nil {
		renderErr("Could not hash the new password.")
		return
	}
	if err := s.store.UpdateUserPassword(user.ID, string(hash)); err != nil {
		renderErr("Could not save the new password.")
		return
	}
	http.Redirect(w, r, "/password?changed=1", http.StatusSeeOther)
}
