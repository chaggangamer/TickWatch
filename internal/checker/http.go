package checker

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/chaggangamer/tickwatch/internal/store"
)

const maxBodyRead = 2 << 20

func (r *localRunner) runHTTP(ctx context.Context, m store.Monitor) Result {
	cfg, err := store.ParseHTTPConfig(m.Config)
	if err != nil {
		return fail(fmt.Sprintf("invalid config: %v", err))
	}
	method := cfg.Method
	if method == "" {
		method = "GET"
	}
	var body io.Reader
	if cfg.Body != "" {
		body = strings.NewReader(cfg.Body)
	}
	req, err := http.NewRequestWithContext(ctx, method, cfg.URL, body)
	if err != nil {
		return fail(fmt.Sprintf("invalid request: %v", err))
	}
	for k, v := range cfg.Headers {
		if strings.EqualFold(k, "Host") {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}

	client := r.clientFollow
	if !cfg.Redirects() {
		client = r.clientDirect
	}

	start := time.Now()
	resp, err := client.Do(req)
	latency := time.Since(start)
	if err != nil {
		return Result{OK: false, LatencyMS: ms(latency), Error: describeError(err)}
	}
	defer resp.Body.Close()

	res := Result{
		StatusCode:   resp.StatusCode,
		LatencyMS:    ms(latency),
		SSLExpiresAt: sslExpiry(resp),
	}

	if !cfg.MatchStatus(resp.StatusCode) {
		res.Error = fmt.Sprintf("unexpected status code: %d", resp.StatusCode)
		return res
	}

	if cfg.BodyMatch != "" || cfg.BodyRegex != "" {
		raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyRead))
		if err != nil {
			res.Error = fmt.Sprintf("read response body: %v", err)
			return res
		}
		if cfg.BodyMatch != "" && !strings.Contains(string(raw), cfg.BodyMatch) {
			res.Error = "body does not contain expected text"
			return res
		}
		if cfg.BodyRegex != "" {
			re, err := regexp.Compile(cfg.BodyRegex)
			if err != nil {
				res.Error = fmt.Sprintf("invalid body_regex: %v", err)
				return res
			}
			if !re.Match(raw) {
				res.Error = "body does not match expected pattern"
				return res
			}
		}
	}

	res.OK = true
	return res
}
