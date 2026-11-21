package store

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type HTTPConfig struct {
	URL             string            `json:"url"`
	Method          string            `json:"method"`
	Headers         map[string]string `json:"headers"`
	Body            string            `json:"body"`
	ExpectStatus    string            `json:"expect_status"`
	BodyMatch       string            `json:"body_match"`
	BodyRegex       string            `json:"body_regex"`
	FollowRedirects *bool             `json:"follow_redirects"`
}

func (c HTTPConfig) Redirects() bool {
	return c.FollowRedirects == nil || *c.FollowRedirects
}

func (c HTTPConfig) MatchStatus(code int) bool {
	ranges, err := ParseStatusRanges(c.ExpectStatus)
	if err != nil {
		return false
	}
	for _, r := range ranges {
		if code >= r[0] && code <= r[1] {
			return true
		}
	}
	return false
}

func ParseStatusRanges(s string) ([][2]int, error) {
	if strings.TrimSpace(s) == "" {
		return [][2]int{{200, 299}}, nil
	}
	var ranges [][2]int
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if lo, hi, found := strings.Cut(part, "-"); found {
			l, err1 := strconv.Atoi(strings.TrimSpace(lo))
			h, err2 := strconv.Atoi(strings.TrimSpace(hi))
			if err1 != nil || err2 != nil || l < 100 || h > 599 || l > h {
				return nil, fmt.Errorf("invalid status range %q", part)
			}
			ranges = append(ranges, [2]int{l, h})
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil || n < 100 || n > 599 {
			return nil, fmt.Errorf("invalid status code %q", part)
		}
		ranges = append(ranges, [2]int{n, n})
	}
	return ranges, nil
}

type TCPConfig struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

type PingConfig struct {
	Host string `json:"host"`
}

type HeartbeatConfig struct {
	Period int `json:"period"`
	Grace  int `json:"grace"`
}

var validMethods = map[string]bool{
	"GET": true, "HEAD": true, "POST": true, "PUT": true,
	"PATCH": true, "DELETE": true, "OPTIONS": true,
}

var hostPattern = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9.-]*[a-zA-Z0-9])?$`)
var headerNamePattern = regexp.MustCompile(`^[!#$%&'*+\-.^_` + "`" + `|~0-9A-Za-z]+$`)

func ParseHTTPConfig(b []byte) (HTTPConfig, error) {
	var c HTTPConfig
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("parse http config: %w", err)
	}
	return c, nil
}

func ParseTCPConfig(b []byte) (TCPConfig, error) {
	var c TCPConfig
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("parse tcp config: %w", err)
	}
	return c, nil
}

func ParsePingConfig(b []byte) (PingConfig, error) {
	var c PingConfig
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("parse ping config: %w", err)
	}
	return c, nil
}

func ParseHeartbeatConfig(b []byte) (HeartbeatConfig, error) {
	var c HeartbeatConfig
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("parse heartbeat config: %w", err)
	}
	return c, nil
}

func DefaultHTTPConfig() HTTPConfig {
	return HTTPConfig{Method: "GET", ExpectStatus: "200-299"}
}

func Normalize(m *Monitor) {
	now := time.Now().Unix()
	if m.Interval == 0 {
		m.Interval = 60
	}
	if m.Timeout == 0 {
		m.Timeout = 10
	}
	if m.FailureThreshold == 0 {
		m.FailureThreshold = 3
	}
	if m.Cooldown == 0 {
		m.Cooldown = 300
	}
	if m.Status == "" {
		m.Status = StatusUp
	}
	if m.CreatedAt == 0 {
		m.CreatedAt = now
	}
	m.UpdatedAt = now
	if m.NextRunAt == 0 {
		m.NextRunAt = now
	}
	if m.Type == TypeHeartbeat && m.NextDeadlineAt == 0 && len(m.Config) > 0 {
		m.NextDeadlineAt = now + int64(m.HeartbeatPeriod()+m.HeartbeatGrace())
	}
}

func (m *Monitor) HeartbeatPeriod() int {
	c, err := ParseHeartbeatConfig(m.Config)
	if err != nil {
		return 0
	}
	return c.Period
}

func (m *Monitor) HeartbeatGrace() int {
	c, err := ParseHeartbeatConfig(m.Config)
	if err != nil {
		return 0
	}
	return c.Grace
}

func (m *Monitor) Target() string {
	switch m.Type {
	case TypeHTTP:
		c, err := ParseHTTPConfig(m.Config)
		if err != nil {
			return ""
		}
		return c.URL
	case TypeTCP:
		c, _ := ParseTCPConfig(m.Config)
		return fmt.Sprintf("%s:%d", c.Host, c.Port)
	case TypePing:
		c, _ := ParsePingConfig(m.Config)
		return c.Host
	default:
		return ""
	}
}

func (m *Monitor) Validate() error {
	if len(m.Name) < 1 || len(m.Name) > 120 {
		return fmt.Errorf("name must be between 1 and 120 characters")
	}
	switch m.Type {
	case TypeHTTP, TypeTCP, TypePing, TypeHeartbeat:
	default:
		return fmt.Errorf("unknown monitor type %q", m.Type)
	}
	if m.Interval < 20 || m.Interval > 604800 {
		return fmt.Errorf("interval_seconds must be between 20 and 604800")
	}
	if m.Timeout < 1 || m.Timeout > 120 {
		return fmt.Errorf("timeout_seconds must be between 1 and 120")
	}
	if m.Retries < 0 || m.Retries > 5 {
		return fmt.Errorf("retries must be between 0 and 5")
	}
	if m.FailureThreshold < 1 || m.FailureThreshold > 10 {
		return fmt.Errorf("failure_threshold must be between 1 and 10")
	}
	if m.Cooldown < 0 || m.Cooldown > 86400 {
		return fmt.Errorf("cooldown_seconds must be between 0 and 86400")
	}
	switch m.Type {
	case TypeHTTP:
		return validateHTTP(m.Config)
	case TypeTCP:
		return validateTCP(m.Config)
	case TypePing:
		return validatePing(m.Config)
	case TypeHeartbeat:
		return validateHeartbeat(m.Config)
	}
	return nil
}

func validateHTTP(b []byte) error {
	c, err := ParseHTTPConfig(b)
	if err != nil {
		return err
	}
	u, err := url.Parse(c.URL)
	if err != nil {
		return fmt.Errorf("invalid url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("url scheme must be http or https")
	}
	if u.Host == "" {
		return fmt.Errorf("url must include a host")
	}
	method := c.Method
	if method == "" {
		method = "GET"
	}
	if !validMethods[method] {
		return fmt.Errorf("unsupported method %q", method)
	}
	if _, err := ParseStatusRanges(c.ExpectStatus); err != nil {
		return err
	}
	if len(c.Body) > 64*1024 {
		return fmt.Errorf("body must be at most 64 KiB")
	}
	if c.BodyMatch != "" && c.BodyRegex != "" {
		return fmt.Errorf("body_match and body_regex are mutually exclusive")
	}
	if c.BodyRegex != "" {
		if _, err := regexp.Compile(c.BodyRegex); err != nil {
			return fmt.Errorf("invalid body_regex: %w", err)
		}
	}
	for k, v := range c.Headers {
		if !headerNamePattern.MatchString(k) {
			return fmt.Errorf("invalid header name %q", k)
		}
		if strings.ContainsAny(v, "\r\n") {
			return fmt.Errorf("invalid header value for %q", k)
		}
	}
	return nil
}

func validateTCP(b []byte) error {
	c, err := ParseTCPConfig(b)
	if err != nil {
		return err
	}
	return validateHostPort(c.Host, c.Port)
}

func validatePing(b []byte) error {
	c, err := ParsePingConfig(b)
	if err != nil {
		return err
	}
	return validateHost(c.Host)
}

func validateHeartbeat(b []byte) error {
	c, err := ParseHeartbeatConfig(b)
	if err != nil {
		return err
	}
	if c.Period < 60 || c.Period > 604800 {
		return fmt.Errorf("period must be between 60 and 604800 seconds")
	}
	if c.Grace < 0 || c.Grace > 86400 {
		return fmt.Errorf("grace must be between 0 and 86400 seconds")
	}
	return nil
}

func validateHostPort(host string, port int) error {
	if err := validateHost(host); err != nil {
		return err
	}
	if port < 1 || port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	return nil
}

func validateHost(host string) error {
	if host == "" || len(host) > 253 {
		return fmt.Errorf("host must be between 1 and 253 characters")
	}
	if !hostPattern.MatchString(host) && !isIPv6Literal(host) {
		return fmt.Errorf("invalid host %q", host)
	}
	return nil
}

func isIPv6Literal(host string) bool {
	if !strings.Contains(host, ":") {
		return false
	}
	segments := strings.Split(host, ":")
	if len(segments) < 3 || len(segments) > 8 {
		return false
	}
	for _, seg := range segments {
		if seg == "" {
			continue
		}
		if len(seg) > 4 {
			return false
		}
		for _, r := range seg {
			if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
				return false
			}
		}
	}
	return true
}
