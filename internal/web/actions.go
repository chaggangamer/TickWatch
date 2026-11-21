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
)

func parseHeaders(lines string) (map[string]string, error) {
	headers := map[string]string{}
	for _, line := range strings.Split(lines, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		k, v, found := strings.Cut(line, ":")
		if !found {
			return nil, &store.ValidationError{Field: "headers", Msg: "use 'Name: value' lines"}
		}
		headers[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return headers, nil
}

func formInt(r *http.Request, key string, def int) int {
	v := r.FormValue(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func formBool(r *http.Request, key string) bool {
	return r.FormValue(key) == "on" || r.FormValue(key) == "1" || r.FormValue(key) == "true"
}

func (s *server) buildMonitorFromForm(r *http.Request, existing *store.Monitor) (*store.Monitor, error) {
	m := &store.Monitor{}
	if existing != nil {
		*m = *existing
	} else {
		m.Type = r.FormValue("type")
		if m.Type == store.TypeHeartbeat {
			m.HeartbeatToken = store.NewToken(16)
		}
	}
	m.Name = r.FormValue("name")
	m.Interval = formInt(r, "interval", 60)
	m.Timeout = formInt(r, "timeout", 10)
	m.Retries = formInt(r, "retries", 0)
	m.FailureThreshold = formInt(r, "failure_threshold", 3)
	m.Cooldown = formInt(r, "cooldown", 300)
	m.IsPublic = formBool(r, "is_public")
	if r.FormValue("enabled_sent") != "" {
		m.Enabled = formBool(r, "enabled")
	} else if existing == nil {
		m.Enabled = true
	}
	return m, nil
}

func configFromForm(r *http.Request, m *store.Monitor) error {
	switch m.Type {
	case store.TypeHTTP:
		headers, err := parseHeaders(r.FormValue("headers"))
		if err != nil {
			return err
		}
		cfg := store.HTTPConfig{
			URL:          r.FormValue("url"),
			Method:       r.FormValue("method"),
			Headers:      headers,
			Body:         r.FormValue("body"),
			ExpectStatus: r.FormValue("expect_status"),
			BodyMatch:    r.FormValue("body_match"),
			BodyRegex:    r.FormValue("body_regex"),
		}
		follow := formBool(r, "follow_redirects")
		cfg.FollowRedirects = &follow
		raw, err := json.Marshal(cfg)
		if err != nil {
			return err
		}
		m.Config = raw
	case store.TypeTCP:
		port, err := strconv.Atoi(r.FormValue("tcp_port"))
		if err != nil {
			return &store.ValidationError{Field: "tcp_port", Msg: "must be a number"}
		}
		raw, err := json.Marshal(store.TCPConfig{Host: r.FormValue("tcp_host"), Port: port})
		if err != nil {
			return err
		}
		m.Config = raw
	case store.TypePing:
		raw, err := json.Marshal(store.PingConfig{Host: r.FormValue("ping_host")})
		if err != nil {
			return err
		}
		m.Config = raw
	case store.TypeHeartbeat:
		raw, err := json.Marshal(store.HeartbeatConfig{
			Period: formInt(r, "period", 3600),
			Grace:  formInt(r, "grace", 300),
		})
		if err != nil {
			return err
		}
		m.Config = raw
	}
	return nil
}

func (s *server) monitorCreate(w http.ResponseWriter, r *http.Request) {
	if !s.csrfCheck(r) {
		s.formError(w, r, nil, errors.New("invalid form token, please retry"))
		return
	}
	m, err := s.buildMonitorFromForm(r, nil)
	if err == nil {
		err = configFromForm(r, m)
	}
	if err == nil {
		store.Normalize(m)
		err = m.Validate()
	}
	if err != nil {
		s.formError(w, r, m, err)
		return
	}
	if err := s.store.CreateMonitor(m); err != nil {
		s.formError(w, r, m, errors.New("could not save monitor"))
		return
	}
	s.linkChannels(r, m.ID)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *server) monitorUpdate(w http.ResponseWriter, r *http.Request) {
	if !s.csrfCheck(r) {
		s.errorPage(w, r, http.StatusForbidden, "Forbidden", "Your session expired or the form token was missing. Go back and try again.")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	existing, err := s.store.Monitor(id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.errorPage(w, r, http.StatusInternalServerError, "Something went wrong", "Could not load the monitor. Check the server logs.")
		return
	}
	m, err := s.buildMonitorFromForm(r, existing)
	if err == nil {
		err = configFromForm(r, m)
	}
	if err == nil {
		store.Normalize(m)
		err = m.Validate()
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if m.Type == store.TypeHeartbeat {
		m.NextDeadlineAt = time.Now().Unix() + int64(m.HeartbeatPeriod()+m.HeartbeatGrace())
		s.tracker.Reset(m.ID)
	}
	if err := s.store.UpdateMonitor(m); err != nil {
		s.errorPage(w, r, http.StatusInternalServerError, "Something went wrong", "Could not save the monitor.")
		return
	}
	s.linkChannels(r, m.ID)
	http.Redirect(w, r, "/monitors/"+strconv.FormatInt(m.ID, 10), http.StatusSeeOther)
}

func (s *server) monitorDelete(w http.ResponseWriter, r *http.Request) {
	if !s.csrfCheck(r) {
		s.errorPage(w, r, http.StatusForbidden, "Forbidden", "Your session expired or the form token was missing. Go back and try again.")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.store.DeleteMonitor(id); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.errorPage(w, r, http.StatusInternalServerError, "Something went wrong", "Could not complete the delete.")
		return
	}
	s.tracker.Reset(id)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *server) monitorPause(w http.ResponseWriter, r *http.Request) {
	if !s.csrfCheck(r) {
		s.errorPage(w, r, http.StatusForbidden, "Forbidden", "Your session expired or the form token was missing. Go back and try again.")
		return
	}
	s.setPaused(w, r, false)
}

func (s *server) monitorResume(w http.ResponseWriter, r *http.Request) {
	if !s.csrfCheck(r) {
		s.errorPage(w, r, http.StatusForbidden, "Forbidden", "Your session expired or the form token was missing. Go back and try again.")
		return
	}
	s.setPaused(w, r, true)
}

func (s *server) setPaused(w http.ResponseWriter, r *http.Request, resume bool) {
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
	m.Enabled = resume
	if resume {
		m.NextRunAt = time.Now().Unix()
		s.tracker.Reset(m.ID)
	}
	if err := s.store.UpdateMonitor(m); err != nil {
		s.errorPage(w, r, http.StatusInternalServerError, "Something went wrong", "Could not save the monitor.")
		return
	}
	back := "/"
	if ref := r.Header.Get("Referer"); ref != "" {
		back = ref
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

func (s *server) linkChannels(r *http.Request, monitorID int64) {
	if r.FormValue("channels_sent") == "" {
		return
	}
	var ids []int64
	for _, part := range r.Form["channels"] {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if id, err := strconv.ParseInt(part, 10, 64); err == nil {
			ids = append(ids, id)
		}
	}
	if err := s.store.SetMonitorChannels(monitorID, ids); err != nil {
		s.log.Warn("link channels failed", "monitor_id", monitorID, "err", err)
	}
}

func (s *server) formError(w http.ResponseWriter, r *http.Request, m *store.Monitor, err error) {
	sess := sessionFrom(r.Context())
	fieldErrs := map[string]string{}
	msg := err.Error()
	var ve *store.ValidationError
	if errors.As(err, &ve) {
		fieldErrs[ve.Field] = ve.Msg
		msg = ve.Field + " " + ve.Msg
	}
	data := map[string]any{
		"Meta":        pageMeta{CSRF: sess.csrf, Title: "Add monitor"},
		"IsEdit":      false,
		"Error":       msg,
		"FieldErrors": fieldErrs,
		"Channels":    s.channelList(),
		"Selected":    map[int64]bool{},
		"HTTP":        store.HTTPConfig{Method: "GET", ExpectStatus: "200-299"},
	}
	if m != nil {
		data["Monitor"] = m
		if m.Type == store.TypeHTTP {
			if hc, perr := store.ParseHTTPConfig(m.Config); perr == nil {
				data["HTTP"] = hc
				data["HeaderLines"] = headerLines(hc.Headers)
			}
		}
		if m.Type == store.TypeTCP {
			if tc, perr := store.ParseTCPConfig(m.Config); perr == nil {
				data["TCP"] = tc
			}
		}
		if m.Type == store.TypePing {
			if pc, perr := store.ParsePingConfig(m.Config); perr == nil {
				data["Ping"] = pc
			}
		}
		if m.Type == store.TypeHeartbeat {
			if hb, perr := store.ParseHeartbeatConfig(m.Config); perr == nil {
				data["Heartbeat"] = hb
			}
		}
	}
	s.render(w, r, 400, "monitor_form", data)
}

func splitTo(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func (s *server) channelConfigFromForm(r *http.Request, chType string, old map[string]any) (map[string]any, error) {
	cfg := map[string]any{}
	switch chType {
	case "telegram":
		cfg["chat_id"] = r.FormValue("chat_id")
		if tok := r.FormValue("bot_token"); tok != "" && tok != "*****" {
			cfg["bot_token"] = tok
		} else if v, ok := old["bot_token"]; ok {
			cfg["bot_token"] = v
		}
	case "slack":
		url := r.FormValue("slack_webhook_url")
		if url == "*****" || url == "" {
			if v, ok := old["webhook_url"].(string); ok {
				url = v
			}
		}
		cfg["webhook_url"] = url
	case "discord":
		url := r.FormValue("discord_webhook_url")
		if url == "*****" || url == "" {
			if v, ok := old["webhook_url"].(string); ok {
				url = v
			}
		}
		cfg["webhook_url"] = url
	case "smtp":
		cfg["host"] = r.FormValue("host")
		cfg["port"] = formInt(r, "port", 587)
		cfg["username"] = r.FormValue("username")
		cfg["from"] = r.FormValue("from")
		cfg["to"] = splitTo(r.FormValue("to"))
		if pw := r.FormValue("password"); pw != "" && pw != "*****" {
			cfg["password"] = pw
		} else if v, ok := old["password"]; ok {
			cfg["password"] = v
		}
	case "webhook":
		cfg["url"] = r.FormValue("url")
		if sec := r.FormValue("secret"); sec != "" && sec != "*****" {
			cfg["secret"] = sec
		} else if v, ok := old["secret"]; ok {
			cfg["secret"] = v
		}
	default:
		return nil, errors.New("unknown channel type")
	}
	return cfg, nil
}

func oldConfig(c *store.Channel) map[string]any {
	var m map[string]any
	if err := json.Unmarshal(c.Config, &m); err != nil || m == nil {
		return map[string]any{}
	}
	return m
}

func (s *server) channelsListForPage() []map[string]any {
	channels, _ := s.store.Channels()
	masked := make([]map[string]any, 0, len(channels))
	for _, c := range channels {
		cfg, _ := json.Marshal(map[string]any{
			"id": c.ID, "type": c.Type, "name": c.Name,
			"config": maskChannel(c),
		})
		masked = append(masked, map[string]any{
			"ID":     c.ID,
			"Type":   c.Type,
			"Name":   c.Name,
			"Config": string(cfg),
		})
	}
	return masked
}

func maskChannel(c store.Channel) map[string]any {
	var m map[string]any
	if err := json.Unmarshal(c.Config, &m); err != nil || m == nil {
		m = map[string]any{}
	}
	for _, key := range channelSecretKeys(c.Type) {
		if v, ok := m[key].(string); ok && v != "" {
			m[key] = "*****"
		}
	}
	return m
}

func channelSecretKeys(chType string) []string {
	switch chType {
	case "telegram":
		return []string{"bot_token"}
	case "slack", "discord":
		return []string{"webhook_url"}
	case "smtp":
		return []string{"password"}
	case "webhook":
		return []string{"secret"}
	}
	return nil
}

func (s *server) channelSave(w http.ResponseWriter, r *http.Request, id int64) {
	if !s.csrfCheck(r) {
		s.errorPage(w, r, http.StatusForbidden, "Forbidden", "Your session expired or the form token was missing. Go back and try again.")
		return
	}
	chType := r.FormValue("type")
	name := r.FormValue("name")
	if name == "" {
		http.Error(w, "name is required", 400)
		return
	}
	var existing *store.Channel
	if id > 0 {
		c, err := s.store.Channel(id)
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			s.errorPage(w, r, http.StatusInternalServerError, "Something went wrong", "Could not load the channel.")
			return
		}
		existing = c
	}
	cfg, err := s.channelConfigFromForm(r, chType, oldConfigFor(existing))
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		http.Error(w, "invalid config", 400)
		return
	}
	if existing == nil {
		c := &store.Channel{Type: chType, Name: name, Config: raw, CreatedAt: time.Now().Unix()}
		if err := s.store.CreateChannel(c); err != nil {
			s.errorPage(w, r, http.StatusInternalServerError, "Something went wrong", "Could not save the channel.")
			return
		}
	} else {
		existing.Type = chType
		existing.Name = name
		existing.Config = raw
		if err := s.store.UpdateChannel(existing); err != nil {
			s.errorPage(w, r, http.StatusInternalServerError, "Something went wrong", "Could not save the channel.")
			return
		}
	}
	http.Redirect(w, r, "/channels", http.StatusSeeOther)
}

func oldConfigFor(existing *store.Channel) map[string]any {
	if existing == nil {
		return map[string]any{}
	}
	return oldConfig(existing)
}

func (s *server) channelCreate(w http.ResponseWriter, r *http.Request) {
	s.channelSave(w, r, 0)
}

func (s *server) channelUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.channelSave(w, r, id)
}

func (s *server) channelDelete(w http.ResponseWriter, r *http.Request) {
	if !s.csrfCheck(r) {
		s.errorPage(w, r, http.StatusForbidden, "Forbidden", "Your session expired or the form token was missing. Go back and try again.")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.store.DeleteChannel(id); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.errorPage(w, r, http.StatusInternalServerError, "Something went wrong", "Could not complete the delete.")
		return
	}
	http.Redirect(w, r, "/channels", http.StatusSeeOther)
}

func (s *server) channelTest(w http.ResponseWriter, r *http.Request) {
	if !s.csrfCheck(r) {
		writeWErr(w, http.StatusForbidden, "csrf", "invalid csrf token")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeWErr(w, 400, "invalid_id", "channel id must be a positive integer")
		return
	}
	if err := s.alerts.SendTest(id); err != nil {
		writeWErr(w, 502, "send_failed", err.Error())
		return
	}
	writeJSONOK(w, map[string]any{"ok": true})
}
