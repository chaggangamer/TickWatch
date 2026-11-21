package store

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"time"

	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")

type DB struct {
	db *sql.DB
}

func Open(path string, log *slog.Logger) (Store, error) {
	dsn := "file:" + filepath.ToSlash(path) +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	s := &DB{db: db}
	if err := migrate(db, log); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *DB) Close() error {
	return s.db.Close()
}

func (s *DB) Ping() error {
	if err := s.db.Ping(); err != nil {
		return fmt.Errorf("database ping: %w", err)
	}
	return nil
}

const monitorCols = `id, type, name, config, interval_seconds, timeout_seconds, retries,
	failure_threshold, cooldown_seconds, enabled, is_public, status, ssl_expires_at,
	last_check_at, next_run_at, last_ping_at, next_deadline_at, heartbeat_token,
	created_at, updated_at`

func scanMonitor(scan func(dest ...any) error) (Monitor, error) {
	var m Monitor
	var hb sql.NullString
	err := scan(&m.ID, &m.Type, &m.Name, &m.Config, &m.Interval, &m.Timeout, &m.Retries,
		&m.FailureThreshold, &m.Cooldown, &m.Enabled, &m.IsPublic, &m.Status, &m.SSLExpiresAt,
		&m.LastCheckAt, &m.NextRunAt, &m.LastPingAt, &m.NextDeadlineAt, &hb,
		&m.CreatedAt, &m.UpdatedAt)
	m.HeartbeatToken = hb.String
	return m, err
}

func (s *DB) CreateMonitor(m *Monitor) error {
	var hb any
	if m.HeartbeatToken != "" {
		hb = m.HeartbeatToken
	}
	res, err := s.db.Exec(`INSERT INTO monitors
		(type, name, config, interval_seconds, timeout_seconds, retries, failure_threshold,
		 cooldown_seconds, enabled, is_public, status, ssl_expires_at, last_check_at,
		 next_run_at, last_ping_at, next_deadline_at, heartbeat_token, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		m.Type, m.Name, string(m.Config), m.Interval, m.Timeout, m.Retries,
		m.FailureThreshold, m.Cooldown, m.Enabled, m.IsPublic, m.Status, m.SSLExpiresAt,
		m.LastCheckAt, m.NextRunAt, m.LastPingAt, m.NextDeadlineAt, hb,
		m.CreatedAt, m.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert monitor: %w", err)
	}
	m.ID, err = res.LastInsertId()
	if err != nil {
		return fmt.Errorf("monitor id: %w", err)
	}
	return nil
}

func (s *DB) UpdateMonitor(m *Monitor) error {
	m.UpdatedAt = time.Now().Unix()
	res, err := s.db.Exec(`UPDATE monitors SET name=?, config=?, interval_seconds=?,
		timeout_seconds=?, retries=?, failure_threshold=?, cooldown_seconds=?,
		enabled=?, is_public=?, updated_at=? WHERE id=?`,
		m.Name, string(m.Config), m.Interval, m.Timeout, m.Retries,
		m.FailureThreshold, m.Cooldown, m.Enabled, m.IsPublic, m.UpdatedAt, m.ID)
	if err != nil {
		return fmt.Errorf("update monitor: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *DB) DeleteMonitor(id int64) error {
	res, err := s.db.Exec(`DELETE FROM monitors WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("delete monitor: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *DB) Monitor(id int64) (*Monitor, error) {
	m, err := scanMonitor(s.db.QueryRow(`SELECT `+monitorCols+` FROM monitors WHERE id=?`, id).Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get monitor: %w", err)
	}
	return &m, nil
}

func (s *DB) MonitorByToken(token string) (*Monitor, error) {
	m, err := scanMonitor(s.db.QueryRow(
		`SELECT `+monitorCols+` FROM monitors WHERE heartbeat_token=? AND type='heartbeat'`, token).Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get monitor by token: %w", err)
	}
	return &m, nil
}

func (s *DB) queryMonitors(query string, args ...any) ([]Monitor, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query monitors: %w", err)
	}
	defer rows.Close()
	var out []Monitor
	for rows.Next() {
		m, err := scanMonitor(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scan monitor: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *DB) Monitors() ([]Monitor, error) {
	return s.queryMonitors(`SELECT ` + monitorCols + ` FROM monitors ORDER BY name`)
}

func (s *DB) PublicMonitors() ([]Monitor, error) {
	return s.queryMonitors(`SELECT ` + monitorCols + ` FROM monitors WHERE is_public=1 AND enabled=1 ORDER BY name`)
}

func (s *DB) DueMonitors(now int64, limit int) ([]Monitor, error) {
	return s.queryMonitors(`SELECT `+monitorCols+` FROM monitors
		WHERE enabled=1 AND type != 'heartbeat' AND next_run_at <= ?
		ORDER BY next_run_at LIMIT ?`, now, limit)
}

func (s *DB) HeartbeatsDue(now int64) ([]Monitor, error) {
	return s.queryMonitors(`SELECT `+monitorCols+` FROM monitors
		WHERE enabled=1 AND type='heartbeat' AND status='up' AND next_deadline_at > 0 AND next_deadline_at <= ?
		ORDER BY next_deadline_at LIMIT 200`, now)
}

func (s *DB) HeartbeatsStale(now int64, minAge int64) ([]Monitor, error) {
	return s.queryMonitors(`SELECT `+monitorCols+` FROM monitors
		WHERE enabled=1 AND type='heartbeat' AND status='down' AND last_check_at <= ?
		ORDER BY last_check_at LIMIT 200`, now-minAge)
}

func (s *DB) SaveRuntime(m *Monitor) error {
	_, err := s.db.Exec(`UPDATE monitors SET status=?, ssl_expires_at=?, last_check_at=?,
		next_run_at=?, last_ping_at=?, next_deadline_at=? WHERE id=?`,
		m.Status, m.SSLExpiresAt, m.LastCheckAt, m.NextRunAt, m.LastPingAt, m.NextDeadlineAt, m.ID)
	if err != nil {
		return fmt.Errorf("save monitor runtime: %w", err)
	}
	return nil
}

func (s *DB) RecordResult(r *CheckResult) error {
	res, err := s.db.Exec(`INSERT INTO check_results (monitor_id, ok, status_code, latency_ms, error, created_at)
		VALUES (?,?,?,?,?,?)`,
		r.MonitorID, r.OK, r.StatusCode, r.LatencyMS, r.Error, r.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert result: %w", err)
	}
	r.ID, err = res.LastInsertId()
	if err != nil {
		return fmt.Errorf("result id: %w", err)
	}
	return nil
}

const resultCols = `id, monitor_id, ok, status_code, latency_ms, error, created_at`

const resultColsRef = `r.id, r.monitor_id, r.ok, r.status_code, r.latency_ms, r.error, r.created_at`

func scanResult(scan func(dest ...any) error) (CheckResult, error) {
	var r CheckResult
	err := scan(&r.ID, &r.MonitorID, &r.OK, &r.StatusCode, &r.LatencyMS, &r.Error, &r.CreatedAt)
	return r, err
}

func (s *DB) Results(monitorID int64, page, perPage int) ([]CheckResult, int64, error) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 || perPage > 100 {
		perPage = 50
	}
	var total int64
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM check_results WHERE monitor_id=?`, monitorID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count results: %w", err)
	}
	rows, err := s.db.Query(`SELECT `+resultCols+` FROM check_results WHERE monitor_id=?
		ORDER BY id DESC LIMIT ? OFFSET ?`, monitorID, perPage, (page-1)*perPage)
	if err != nil {
		return nil, 0, fmt.Errorf("query results: %w", err)
	}
	defer rows.Close()
	var out []CheckResult
	for rows.Next() {
		r, err := scanResult(rows.Scan)
		if err != nil {
			return nil, 0, fmt.Errorf("scan result: %w", err)
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

func (s *DB) RecentResults(monitorID int64, limit int) ([]CheckResult, error) {
	rows, err := s.db.Query(`SELECT `+resultCols+` FROM check_results WHERE monitor_id=?
		ORDER BY id DESC LIMIT ?`, monitorID, limit)
	if err != nil {
		return nil, fmt.Errorf("query recent results: %w", err)
	}
	defer rows.Close()
	var out []CheckResult
	for rows.Next() {
		r, err := scanResult(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scan result: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *DB) LatestResults() (map[int64]CheckResult, error) {
	rows, err := s.db.Query(`SELECT ` + resultColsRef + ` FROM check_results r
		JOIN (SELECT monitor_id, MAX(id) AS rid FROM check_results GROUP BY monitor_id) last
		ON r.id = last.rid`)
	if err != nil {
		return nil, fmt.Errorf("query latest results: %w", err)
	}
	defer rows.Close()
	out := map[int64]CheckResult{}
	for rows.Next() {
		r, err := scanResult(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scan result: %w", err)
		}
		out[r.MonitorID] = r
	}
	return out, rows.Err()
}

func (s *DB) Uptime(monitorID int64, since int64) (UptimeStat, error) {
	var stat UptimeStat
	if err := s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(ok),0) FROM check_results
		WHERE monitor_id=? AND created_at>=?`, monitorID, since).Scan(&stat.Total, &stat.OK); err != nil {
		return stat, fmt.Errorf("uptime raw: %w", err)
	}
	var total, ok int64
	if err := s.db.QueryRow(`SELECT COALESCE(SUM(total),0), COALESCE(SUM(ok),0) FROM hourly_summaries
		WHERE monitor_id=? AND hour>=?`, monitorID, since).Scan(&total, &ok); err != nil {
		return stat, fmt.Errorf("uptime summaries: %w", err)
	}
	stat.Total += total
	stat.OK += ok
	return stat, nil
}

func (s *DB) UptimeBatch(since int64) (map[int64]UptimeStat, error) {
	out := map[int64]UptimeStat{}
	merge := func(rows *sql.Rows) error {
		for rows.Next() {
			var id, total, ok int64
			if err := rows.Scan(&id, &total, &ok); err != nil {
				return err
			}
			stat := out[id]
			stat.Total += total
			stat.OK += ok
			out[id] = stat
		}
		return rows.Err()
	}
	rows, err := s.db.Query(`SELECT monitor_id, COUNT(*), COALESCE(SUM(ok),0) FROM check_results
		WHERE created_at>=? GROUP BY monitor_id`, since)
	if err != nil {
		return nil, fmt.Errorf("uptime batch raw: %w", err)
	}
	if err := merge(rows); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	rows, err = s.db.Query(`SELECT monitor_id, COALESCE(SUM(total),0), COALESCE(SUM(ok),0)
		FROM hourly_summaries WHERE hour>=? GROUP BY monitor_id`, since)
	if err != nil {
		return nil, fmt.Errorf("uptime batch summaries: %w", err)
	}
	defer rows.Close()
	if err := merge(rows); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *DB) Series(monitorID int64, since int64, maxPoints int) ([]Point, error) {
	if maxPoints < 10 {
		maxPoints = 10
	}
	raw := map[int64][]Point{}
	rows, err := s.db.Query(`SELECT created_at, latency_ms FROM check_results
		WHERE monitor_id=? AND created_at>=? AND ok=1 ORDER BY created_at`, monitorID, since)
	if err != nil {
		return nil, fmt.Errorf("series raw: %w", err)
	}
	for rows.Next() {
		var t, ms int64
		if err := rows.Scan(&t, &ms); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan series: %w", err)
		}
		raw[t/3600] = append(raw[t/3600], Point{Time: t, MS: ms})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate series: %w", err)
	}

	rows, err = s.db.Query(`SELECT hour, latency_sum, total FROM hourly_summaries
		WHERE monitor_id=? AND hour>=? ORDER BY hour`, monitorID, since)
	if err != nil {
		return nil, fmt.Errorf("series summaries: %w", err)
	}
	defer rows.Close()
	var points []Point
	seen := map[int64]bool{}
	for rows.Next() {
		var hour, sum, total int64
		if err := rows.Scan(&hour, &sum, &total); err != nil {
			return nil, fmt.Errorf("scan summary: %w", err)
		}
		if _, has := raw[hour]; has {
			points = append(points, raw[hour]...)
			seen[hour] = true
			continue
		}
		if total > 0 {
			points = append(points, Point{Time: hour, MS: sum / total})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate summaries: %w", err)
	}
	for hour, pts := range raw {
		if !seen[hour] {
			points = append(points, pts...)
		}
	}
	sort.Slice(points, func(i, j int) bool { return points[i].Time < points[j].Time })

	if len(points) > maxPoints {
		stride := len(points) / maxPoints
		if stride < 1 {
			stride = 1
		}
		var sampled []Point
		for i := 0; i < len(points); i += stride {
			sampled = append(sampled, points[i])
		}
		points = sampled
	}
	return points, nil
}

func (s *DB) DailyUptime(monitorID int64, days int, now int64) ([]DayStat, error) {
	todayStart := now - now%86400
	perDay := map[int64]*DayStat{}
	merge := func(rows *sql.Rows) error {
		for rows.Next() {
			var day, total, ok int64
			if err := rows.Scan(&day, &total, &ok); err != nil {
				return err
			}
			stat := perDay[day]
			if stat == nil {
				stat = &DayStat{Day: day}
				perDay[day] = stat
			}
			stat.Total += total
			stat.OK += ok
		}
		return rows.Err()
	}
	rows, err := s.db.Query(`SELECT (hour/86400)*86400, SUM(total), SUM(ok) FROM hourly_summaries
		WHERE monitor_id=? AND hour>=? AND hour<? GROUP BY (hour/86400)*86400`,
		monitorID, todayStart-int64(days)*86400, todayStart)
	if err != nil {
		return nil, fmt.Errorf("daily uptime summaries: %w", err)
	}
	if err := merge(rows); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	rows, err = s.db.Query(`SELECT (created_at/86400)*86400, COUNT(*), SUM(ok) FROM check_results
		WHERE monitor_id=? AND created_at>=? GROUP BY (created_at/86400)*86400`,
		monitorID, todayStart)
	if err != nil {
		return nil, fmt.Errorf("daily uptime raw: %w", err)
	}
	if err := merge(rows); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	out := make([]DayStat, 0, days)
	for i := days - 1; i >= 0; i-- {
		day := todayStart - int64(i)*86400
		if stat, has := perDay[day]; has {
			out = append(out, *stat)
			continue
		}
		out = append(out, DayStat{Day: day})
	}
	return out, nil
}

func (s *DB) OpenIncident(monitorID int64, startedAt int64, reason string) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO incidents (monitor_id, started_at, reason) VALUES (?,?,?)`,
		monitorID, startedAt, reason)
	if err != nil {
		return 0, fmt.Errorf("insert incident: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("incident id: %w", err)
	}
	return id, nil
}

func (s *DB) CloseIncidents(monitorID int64, endedAt int64) error {
	_, err := s.db.Exec(`UPDATE incidents SET ended_at=? WHERE monitor_id=? AND ended_at IS NULL`,
		endedAt, monitorID)
	if err != nil {
		return fmt.Errorf("close incidents: %w", err)
	}
	return nil
}

const incidentCols = `id, monitor_id, started_at, COALESCE(ended_at, 0), reason`

func scanIncident(scan func(dest ...any) error) (Incident, error) {
	var i Incident
	err := scan(&i.ID, &i.MonitorID, &i.StartedAt, &i.EndedAt, &i.Reason)
	return i, err
}

func (s *DB) Incidents(monitorID int64, page, perPage int) ([]Incident, int64, error) {
	return s.queryIncidents(`WHERE monitor_id=?`, monitorID, page, perPage)
}

func (s *DB) AllIncidents(page, perPage int) ([]Incident, int64, error) {
	return s.queryIncidents(``, 0, page, perPage)
}

func (s *DB) queryIncidents(where string, monitorID int64, page, perPage int) ([]Incident, int64, error) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 || perPage > 100 {
		perPage = 50
	}
	args := []any{}
	cond := ""
	if where != "" {
		cond = where
		args = append(args, monitorID)
	}
	var total int64
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM incidents `+cond, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count incidents: %w", err)
	}
	q := `SELECT ` + incidentCols + ` FROM incidents ` + cond + ` ORDER BY id DESC LIMIT ? OFFSET ?`
	args = append(args, perPage, (page-1)*perPage)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("query incidents: %w", err)
	}
	defer rows.Close()
	var out []Incident
	for rows.Next() {
		inc, err := scanIncident(rows.Scan)
		if err != nil {
			return nil, 0, fmt.Errorf("scan incident: %w", err)
		}
		out = append(out, inc)
	}
	return out, total, rows.Err()
}

func (s *DB) OpenIncidents() ([]Incident, error) {
	rows, err := s.db.Query(`SELECT ` + incidentCols + ` FROM incidents WHERE ended_at IS NULL ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("query open incidents: %w", err)
	}
	defer rows.Close()
	var out []Incident
	for rows.Next() {
		inc, err := scanIncident(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scan incident: %w", err)
		}
		out = append(out, inc)
	}
	return out, rows.Err()
}

const channelCols = `id, type, name, config, created_at`

func scanChannel(scan func(dest ...any) error) (Channel, error) {
	var c Channel
	err := scan(&c.ID, &c.Type, &c.Name, &c.Config, &c.CreatedAt)
	return c, err
}

func (s *DB) CreateChannel(c *Channel) error {
	res, err := s.db.Exec(`INSERT INTO channels (type, name, config, created_at) VALUES (?,?,?,?)`,
		c.Type, c.Name, string(c.Config), c.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert channel: %w", err)
	}
	c.ID, err = res.LastInsertId()
	if err != nil {
		return fmt.Errorf("channel id: %w", err)
	}
	return nil
}

func (s *DB) UpdateChannel(c *Channel) error {
	res, err := s.db.Exec(`UPDATE channels SET type=?, name=?, config=? WHERE id=?`,
		c.Type, c.Name, string(c.Config), c.ID)
	if err != nil {
		return fmt.Errorf("update channel: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *DB) DeleteChannel(id int64) error {
	res, err := s.db.Exec(`DELETE FROM channels WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("delete channel: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *DB) Channel(id int64) (*Channel, error) {
	c, err := scanChannel(s.db.QueryRow(`SELECT `+channelCols+` FROM channels WHERE id=?`, id).Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get channel: %w", err)
	}
	return &c, nil
}

func (s *DB) Channels() ([]Channel, error) {
	rows, err := s.db.Query(`SELECT ` + channelCols + ` FROM channels ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("query channels: %w", err)
	}
	defer rows.Close()
	var out []Channel
	for rows.Next() {
		c, err := scanChannel(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scan channel: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *DB) MonitorChannels(monitorID int64) ([]Channel, error) {
	rows, err := s.db.Query(`SELECT c.`+channelCols+` FROM channels c
		JOIN monitor_channels mc ON mc.channel_id = c.id
		WHERE mc.monitor_id=? ORDER BY c.name`, monitorID)
	if err != nil {
		return nil, fmt.Errorf("query monitor channels: %w", err)
	}
	defer rows.Close()
	var out []Channel
	for rows.Next() {
		c, err := scanChannel(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scan channel: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *DB) SetMonitorChannels(monitorID int64, ids []int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM monitor_channels WHERE monitor_id=?`, monitorID); err != nil {
		return fmt.Errorf("clear monitor channels: %w", err)
	}
	for _, id := range ids {
		if _, err := tx.Exec(`INSERT INTO monitor_channels (monitor_id, channel_id) VALUES (?,?)`,
			monitorID, id); err != nil {
			return fmt.Errorf("link channel %d: %w", id, err)
		}
	}
	return tx.Commit()
}

func (s *DB) DownMonitors() ([]Monitor, error) {
	return s.queryMonitors(`SELECT ` + monitorCols + ` FROM monitors
		WHERE enabled=1 AND status='down' ORDER BY id`)
}

func (s *DB) LastAlert(monitorID, channelID int64) (at int64, incidentID int64, ok bool, err error) {
	err = s.db.QueryRow(`SELECT last_sent_at, last_incident_id FROM alert_state WHERE monitor_id=? AND channel_id=?`,
		monitorID, channelID).Scan(&at, &incidentID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, false, nil
	}
	if err != nil {
		return 0, 0, false, fmt.Errorf("get alert state: %w", err)
	}
	return at, incidentID, true, nil
}

func (s *DB) SaveAlert(monitorID, channelID, at int64, incidentID int64) error {
	_, err := s.db.Exec(`INSERT INTO alert_state (monitor_id, channel_id, last_sent_at, last_incident_id) VALUES (?,?,?,?)
		ON CONFLICT (monitor_id, channel_id) DO UPDATE SET
			last_sent_at=excluded.last_sent_at,
			last_incident_id=excluded.last_incident_id`,
		monitorID, channelID, at, incidentID)
	if err != nil {
		return fmt.Errorf("save alert state: %w", err)
	}
	return nil
}

func (s *DB) SSLAlertTargets() ([]Monitor, error) {
	return s.queryMonitors(`SELECT ` + monitorCols + ` FROM monitors
		WHERE enabled=1 AND type='http' AND ssl_expires_at > 0`)
}

func (s *DB) SSLAlerted(monitorID int64, days int) (bool, error) {
	var one int
	err := s.db.QueryRow(`SELECT 1 FROM ssl_alerts WHERE monitor_id=? AND days=?`,
		monitorID, days).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get ssl alert: %w", err)
	}
	return true, nil
}

func (s *DB) MarkSSLAlert(monitorID int64, days int, at int64) error {
	_, err := s.db.Exec(`INSERT INTO ssl_alerts (monitor_id, days, sent_at) VALUES (?,?,?)
		ON CONFLICT (monitor_id, days) DO UPDATE SET sent_at=excluded.sent_at`,
		monitorID, days, at)
	if err != nil {
		return fmt.Errorf("mark ssl alert: %w", err)
	}
	return nil
}

func (s *DB) ClearSSLAlerts(monitorID int64, remainingDays int) error {
	_, err := s.db.Exec(`DELETE FROM ssl_alerts WHERE monitor_id=? AND days < ?`, monitorID, remainingDays)
	if err != nil {
		return fmt.Errorf("clear ssl alerts: %w", err)
	}
	return nil
}

func (s *DB) UserCount() (int, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count users: %w", err)
	}
	return n, nil
}

func (s *DB) CreateUser(u *User) error {
	res, err := s.db.Exec(`INSERT INTO users (username, password_hash, created_at) VALUES (?,?,?)`,
		u.Username, u.PasswordHash, u.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert user: %w", err)
	}
	u.ID, err = res.LastInsertId()
	if err != nil {
		return fmt.Errorf("user id: %w", err)
	}
	return nil
}

func (s *DB) UserByName(name string) (*User, error) {
	var u User
	err := s.db.QueryRow(`SELECT id, username, password_hash, created_at FROM users WHERE username=?`,
		name).Scan(&u.ID, &u.Username, &u.PasswordHash, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}
	return &u, nil
}

func (s *DB) CreateSession(sess *Session) error {
	_, err := s.db.Exec(`INSERT INTO sessions (token_hash, user_id, csrf_token, expires_at) VALUES (?,?,?,?)`,
		sess.TokenHash, sess.UserID, sess.CSRFToken, sess.ExpiresAt)
	if err != nil {
		return fmt.Errorf("insert session: %w", err)
	}
	return nil
}

func (s *DB) Session(tokenHash string) (*Session, error) {
	var sess Session
	err := s.db.QueryRow(`SELECT token_hash, user_id, csrf_token, expires_at FROM sessions WHERE token_hash=?`,
		tokenHash).Scan(&sess.TokenHash, &sess.UserID, &sess.CSRFToken, &sess.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get session: %w", err)
	}
	return &sess, nil
}

func (s *DB) DeleteSession(tokenHash string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE token_hash=?`, tokenHash)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

func (s *DB) DeleteExpiredSessions(now int64) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM sessions WHERE expires_at < ?`, now)
	if err != nil {
		return 0, fmt.Errorf("delete expired sessions: %w", err)
	}
	return res.RowsAffected()
}

func (s *DB) CreateAPIToken(t *APIToken) error {
	res, err := s.db.Exec(`INSERT INTO api_tokens (name, token_hash, created_at, last_used_at) VALUES (?,?,?,0)`,
		t.Name, t.TokenHash, t.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert api token: %w", err)
	}
	t.ID, err = res.LastInsertId()
	if err != nil {
		return fmt.Errorf("api token id: %w", err)
	}
	return nil
}

func (s *DB) APITokens() ([]APIToken, error) {
	rows, err := s.db.Query(`SELECT id, name, token_hash, created_at, last_used_at FROM api_tokens ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("query api tokens: %w", err)
	}
	defer rows.Close()
	var out []APIToken
	for rows.Next() {
		var t APIToken
		if err := rows.Scan(&t.ID, &t.Name, &t.TokenHash, &t.CreatedAt, &t.LastUsedAt); err != nil {
			return nil, fmt.Errorf("scan api token: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *DB) APITokenByHash(hash string) (*APIToken, error) {
	var t APIToken
	err := s.db.QueryRow(`SELECT id, name, token_hash, created_at, last_used_at FROM api_tokens WHERE token_hash=?`,
		hash).Scan(&t.ID, &t.Name, &t.TokenHash, &t.CreatedAt, &t.LastUsedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get api token: %w", err)
	}
	return &t, nil
}

func (s *DB) DeleteAPIToken(id int64) error {
	res, err := s.db.Exec(`DELETE FROM api_tokens WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("delete api token: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *DB) TouchAPIToken(hash string, at int64) error {
	_, err := s.db.Exec(`UPDATE api_tokens SET last_used_at=? WHERE token_hash=? AND last_used_at < ?-60`,
		at, hash, at)
	if err != nil {
		return fmt.Errorf("touch api token: %w", err)
	}
	return nil
}

func (s *DB) AggregateResults(cutoff int64) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin retention tx: %w", err)
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO hourly_summaries (monitor_id, hour, total, ok, latency_sum)
		SELECT monitor_id, (created_at/3600)*3600, COUNT(*), SUM(ok), SUM(latency_ms)
		FROM check_results WHERE created_at < ?
		GROUP BY monitor_id, (created_at/3600)*3600
		ON CONFLICT (monitor_id, hour) DO UPDATE SET
			total = total + excluded.total,
			ok = ok + excluded.ok,
			latency_sum = latency_sum + excluded.latency_sum`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("aggregate results: %w", err)
	}
	res, err := tx.Exec(`DELETE FROM check_results WHERE created_at < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("delete old results: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("retention rows affected: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit retention: %w", err)
	}
	return n, nil
}

func (s *DB) DeleteSummaries(cutoff int64) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM hourly_summaries WHERE hour < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("delete old summaries: %w", err)
	}
	return res.RowsAffected()
}
