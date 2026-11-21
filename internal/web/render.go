package web

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"time"
)

type ctxKey int

const sessionKey ctxKey = 1

func withSession(ctx context.Context, s sessionCtx) context.Context {
	return context.WithValue(ctx, sessionKey, s)
}

func sessionFrom(ctx context.Context) *sessionCtx {
	s, ok := ctx.Value(sessionKey).(sessionCtx)
	if !ok {
		return nil
	}
	return &s
}

func (s *server) parseTemplates() error {
	funcs := template.FuncMap{
		"fmtTime": func(unix int64) string {
			if unix <= 0 {
				return "never"
			}
			return time.Unix(unix, 0).Format("2006-01-02 15:04:05")
		},
		"ago": func(unix int64) string {
			if unix <= 0 {
				return "never"
			}
			d := time.Since(time.Unix(unix, 0))
			switch {
			case d < time.Minute:
				return fmt.Sprintf("%ds ago", int(d.Seconds()))
			case d < time.Hour:
				return fmt.Sprintf("%dm ago", int(d.Minutes()))
			case d < 24*time.Hour:
				return fmt.Sprintf("%dh ago", int(d.Hours()))
			default:
				return fmt.Sprintf("%dd ago", int(d.Hours()/24))
			}
		},
		"dur": func(sec int64) string {
			if sec <= 0 {
				return "0s"
			}
			d := sec / 86400
			h := (sec % 86400) / 3600
			m := (sec % 3600) / 60
			switch {
			case d > 0:
				return fmt.Sprintf("%dd %dh", d, h)
			case h > 0:
				return fmt.Sprintf("%dh %dm", h, m)
			case m > 0:
				return fmt.Sprintf("%dm", m)
			default:
				return fmt.Sprintf("%ds", sec)
			}
		},
		"spark": sparkSVG,
	}

	names := []string{
		"login", "monitors", "monitor_form", "monitor_detail",
		"channels", "status",
	}
	s.tpl = map[string]*template.Template{}
	for _, name := range names {
		t, err := template.New("layout.html").Funcs(funcs).
			ParseFS(templateFS, "templates/layout.html", "templates/"+name+".html")
		if err != nil {
			return fmt.Errorf("parse template %s: %w", name, err)
		}
		s.tpl[name] = t
	}
	return nil
}

func (s *server) render(w http.ResponseWriter, status int, name string, data any) {
	t, ok := s.tpl[name]
	if !ok {
		http.Error(w, "template not found", 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := t.ExecuteTemplate(w, "layout.html", data); err != nil {
		s.log.Error("render template failed", "name", name, "err", err)
	}
}

func sparkSVG(vals []int64) template.HTML {
	if len(vals) == 0 {
		return template.HTML(`<span class="muted">no data</span>`)
	}
	w, h := 120.0, 24.0
	minV, maxV := vals[0], vals[0]
	for _, v := range vals {
		if v < minV {
			minV = v
		}
		if v > maxV {
			maxV = v
		}
	}
	span := float64(maxV - minV)
	if span == 0 {
		span = 1
	}
	step := w / float64(len(vals)-1)
	if len(vals) == 1 {
		step = 0
	}
	var pts string
	for i, v := range vals {
		x := float64(i) * step
		y := h - 2 - (float64(v-minV)/span)*(h-4)
		pts += fmt.Sprintf("%.1f,%.1f ", x, y)
	}
	svg := fmt.Sprintf(`<svg class="spark" width="%.0f" height="%.0f" viewBox="0 0 %.0f %.0f"><polyline points="%s"/></svg>`, w, h, w, h, pts)
	return template.HTML(svg)
}

func sortedKeys(m map[string]float64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
