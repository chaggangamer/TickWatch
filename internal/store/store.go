package store

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"time"
)

const (
	StatusUp   = "up"
	StatusDown = "down"
)

const (
	TypeHTTP      = "http"
	TypeTCP       = "tcp"
	TypePing      = "ping"
	TypeHeartbeat = "heartbeat"
)

type Monitor struct {
	ID               int64  `json:"id"`
	Type             string `json:"type"`
	Name             string `json:"name"`
	Config           []byte `json:"-"`
	Interval         int    `json:"interval_seconds"`
	Timeout          int    `json:"timeout_seconds"`
	Retries          int    `json:"retries"`
	FailureThreshold int    `json:"failure_threshold"`
	Cooldown         int    `json:"cooldown_seconds"`
	Enabled          bool   `json:"enabled"`
	IsPublic         bool   `json:"is_public"`
	Status           string `json:"status"`
	SSLExpiresAt     int64  `json:"ssl_expires_at"`
	LastCheckAt      int64  `json:"last_check_at"`
	NextRunAt        int64  `json:"-"`
	LastPingAt       int64  `json:"last_ping_at"`
	NextDeadlineAt   int64  `json:"-"`
	HeartbeatToken   string `json:"heartbeat_token,omitempty"`
	CreatedAt        int64  `json:"created_at"`
	UpdatedAt        int64  `json:"updated_at"`
}

type CheckResult struct {
	ID         int64  `json:"id"`
	MonitorID  int64  `json:"monitor_id"`
	OK         bool   `json:"ok"`
	StatusCode int    `json:"status_code"`
	LatencyMS  int64  `json:"latency_ms"`
	Error      string `json:"error"`
	CreatedAt  int64  `json:"created_at"`
}

type Incident struct {
	ID        int64  `json:"id"`
	MonitorID int64  `json:"monitor_id"`
	StartedAt int64  `json:"started_at"`
	EndedAt   int64  `json:"ended_at"`
	Reason    string `json:"reason"`
}

func (i Incident) Open() bool { return i.EndedAt == 0 }

func (i Incident) Duration() int64 {
	if i.Open() {
		return time.Now().Unix() - i.StartedAt
	}
	return i.EndedAt - i.StartedAt
}

type Channel struct {
	ID        int64  `json:"id"`
	Type      string `json:"type"`
	Name      string `json:"name"`
	Config    []byte `json:"-"`
	CreatedAt int64  `json:"created_at"`
}

type User struct {
	ID           int64
	Username     string
	PasswordHash string
	CreatedAt    int64
}

type Session struct {
	TokenHash string
	UserID    int64
	CSRFToken string
	ExpiresAt int64
}

type APIToken struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	TokenHash  string `json:"-"`
	CreatedAt  int64  `json:"created_at"`
	LastUsedAt int64  `json:"last_used_at"`
}

type Point struct {
	Time int64 `json:"t"`
	MS   int64 `json:"ms"`
}

type DayStat struct {
	Day   int64
	Total int64
	OK    int64
}

func (d DayStat) Percent() float64 {
	if d.Total == 0 {
		return -1
	}
	return float64(d.OK) / float64(d.Total) * 100
}

type UptimeStat struct {
	Total int64
	OK    int64
}

func (u UptimeStat) Percent() float64 {
	if u.Total == 0 {
		return -1
	}
	return float64(u.OK) / float64(u.Total) * 100
}

type Store interface {
	Close() error
	Ping() error

	CreateMonitor(m *Monitor) error
	UpdateMonitor(m *Monitor) error
	DeleteMonitor(id int64) error
	Monitor(id int64) (*Monitor, error)
	MonitorByToken(token string) (*Monitor, error)
	Monitors() ([]Monitor, error)
	PublicMonitors() ([]Monitor, error)
	DueMonitors(now int64, limit int) ([]Monitor, error)
	HeartbeatsDue(now int64) ([]Monitor, error)
	HeartbeatsStale(now int64, minAge int64) ([]Monitor, error)
	SaveRuntime(m *Monitor) error

	RecordResult(r *CheckResult) error
	Results(monitorID int64, page, perPage int) ([]CheckResult, int64, error)
	RecentResults(monitorID int64, limit int) ([]CheckResult, error)
	LatestResults() (map[int64]CheckResult, error)
	Uptime(monitorID int64, since int64) (UptimeStat, error)
	UptimeBatch(since int64) (map[int64]UptimeStat, error)
	Series(monitorID int64, since int64, maxPoints int) ([]Point, error)
	DailyUptime(monitorID int64, days int, now int64) ([]DayStat, error)

	OpenIncident(monitorID int64, startedAt int64, reason string) (int64, error)
	CloseIncidents(monitorID int64, endedAt int64) error
	Incidents(monitorID int64, page, perPage int) ([]Incident, int64, error)
	AllIncidents(page, perPage int) ([]Incident, int64, error)
	OpenIncidents() ([]Incident, error)

	CreateChannel(c *Channel) error
	UpdateChannel(c *Channel) error
	DeleteChannel(id int64) error
	Channel(id int64) (*Channel, error)
	Channels() ([]Channel, error)
	MonitorChannels(monitorID int64) ([]Channel, error)
	SetMonitorChannels(monitorID int64, ids []int64) error

	DownMonitors() ([]Monitor, error)
	LastAlert(monitorID, channelID int64) (at int64, incidentID int64, ok bool, err error)
	SaveAlert(monitorID, channelID, at int64, incidentID int64) error

	SSLAlertTargets() ([]Monitor, error)
	SSLAlerted(monitorID int64, days int) (bool, error)
	MarkSSLAlert(monitorID int64, days int, at int64) error
	ClearSSLAlerts(monitorID int64, remainingDays int) error

	UserCount() (int, error)
	CreateUser(u *User) error
	UserByName(name string) (*User, error)

	CreateSession(s *Session) error
	Session(tokenHash string) (*Session, error)
	DeleteSession(tokenHash string) error
	DeleteExpiredSessions(now int64) (int64, error)

	CreateAPIToken(t *APIToken) error
	APITokens() ([]APIToken, error)
	APITokenByHash(hash string) (*APIToken, error)
	DeleteAPIToken(id int64) error
	TouchAPIToken(hash string, at int64) error

	AggregateResults(cutoff int64) (int64, error)
	DeleteSummaries(cutoff int64) (int64, error)
}

func NewToken(byteLen int) string {
	b := make([]byte, byteLen)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
