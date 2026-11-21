package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/tickwatch/tickwatch/internal/alert"
	"github.com/tickwatch/tickwatch/internal/checker"
	"github.com/tickwatch/tickwatch/internal/incident"
	"github.com/tickwatch/tickwatch/internal/store"
)

const dueLimit = 200

type Deps struct {
	Store   store.Store
	Runner  checker.Runner
	Tracker *incident.Tracker
	Alerts  *alert.Dispatcher
	Workers int
	Log     *slog.Logger
}

type Scheduler struct {
	store   store.Store
	runner  checker.Runner
	tracker *incident.Tracker
	alerts  *alert.Dispatcher
	log     *slog.Logger
	workers int
	jobs    chan store.Monitor
	wg      sync.WaitGroup
}

func New(d Deps) *Scheduler {
	if d.Log == nil {
		d.Log = slog.Default()
	}
	if d.Workers < 1 {
		d.Workers = 4
	}
	return &Scheduler{
		store:   d.Store,
		runner:  d.Runner,
		tracker: d.Tracker,
		alerts:  d.Alerts,
		log:     d.Log,
		workers: d.Workers,
		jobs:    make(chan store.Monitor, 1024),
	}
}

func (s *Scheduler) Run(ctx context.Context) error {
	if err := s.staggerOverdue(); err != nil {
		return err
	}
	for range s.workers {
		s.wg.Add(1)
		go s.worker(ctx)
	}
	s.wg.Add(1)
	go s.heartbeatLoop(ctx)
	s.wg.Add(1)
	go s.sslLoop(ctx)

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			s.wg.Wait()
			return nil
		case <-ticker.C:
			s.dispatchDue(ctx)
		}
	}
}

func (s *Scheduler) staggerOverdue() error {
	monitors, err := s.store.Monitors()
	if err != nil {
		return fmt.Errorf("load monitors: %w", err)
	}
	now := time.Now().Unix()
	for _, m := range monitors {
		if m.Enabled && m.NextRunAt < now {
			m.NextRunAt = now + int64(rand.IntN(30))
			if err := s.store.SaveRuntime(&m); err != nil {
				return fmt.Errorf("stagger monitor %d: %w", m.ID, err)
			}
		}
	}
	return nil
}

func (s *Scheduler) dispatchDue(ctx context.Context) {
	now := time.Now().Unix()
	due, err := s.store.DueMonitors(now, dueLimit)
	if err != nil {
		s.log.Error("query due monitors failed", "err", err)
		return
	}
	for _, m := range due {
		select {
		case s.jobs <- m:
		default:
			s.log.Warn("worker queue full, skipping check", "monitor_id", m.ID)
		}
	}
}

func (s *Scheduler) worker(ctx context.Context) {
	defer s.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case m := <-s.jobs:
			s.runCheck(m)
		}
	}
}

func (s *Scheduler) runCheck(m store.Monitor) {
	checkCtx, cancel := context.WithTimeout(context.Background(), s.checkBudget(m))
	defer cancel()
	now := time.Now().Unix()

	var res checker.Result
	for attempt := 0; attempt <= m.Retries; attempt++ {
		res = s.runner.Run(checkCtx, m)
		if res.OK {
			break
		}
		if attempt < m.Retries && checkCtx.Err() == nil {
			time.Sleep(time.Duration(attempt+1) * 500 * time.Millisecond)
		}
	}
	if checkCtx.Err() != nil {
		res = checker.Result{OK: false, Error: "request timed out"}
	}
	m.LastCheckAt = now
	if res.SSLExpiresAt > 0 {
		m.SSLExpiresAt = res.SSLExpiresAt
	}
	m.NextRunAt = now + int64(m.Interval) + int64(jitterSeconds(m.Interval))
	if err := s.store.RecordResult(&store.CheckResult{
		MonitorID:  m.ID,
		OK:         res.OK,
		StatusCode: res.StatusCode,
		LatencyMS:  res.LatencyMS,
		Error:      res.Error,
		CreatedAt:  now,
	}); err != nil {
		s.log.Error("record result failed", "monitor_id", m.ID, "err", err)
	}
	ev := s.tracker.Observe(&m, res.OK, res.Error, now)
	s.notifyEvent(ev)
	if err := s.store.SaveRuntime(&m); err != nil {
		s.log.Error("save runtime failed", "monitor_id", m.ID, "err", err)
	}
}

func (s *Scheduler) notifyEvent(ev *incident.Event) {
	if ev == nil {
		return
	}
	switch ev.Kind {
	case incident.EventDown:
		s.alerts.NotifyDown(ev.Monitor, ev.Reason, ev.IncidentID)
	case incident.EventRecovered:
		s.alerts.NotifyRecovered(ev.Monitor, ev.Downtime)
	}
}

func (s *Scheduler) checkBudget(m store.Monitor) time.Duration {
	base := time.Duration(m.Timeout+2) * time.Second
	overhead := time.Duration(m.Retries) * (time.Duration(m.Timeout) * time.Second + time.Second)
	return base + overhead
}

func jitterSeconds(interval int) int {
	pct := interval / 20
	if pct < 1 {
		pct = 1
	}
	return rand.IntN(pct*2+1) - pct
}

func (s *Scheduler) heartbeatLoop(ctx context.Context) {
	defer s.wg.Done()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sweepHeartbeats()
		}
	}
}

func (s *Scheduler) sweepHeartbeats() {
	now := time.Now().Unix()
	due, err := s.store.HeartbeatsDue(now)
	if err != nil {
		s.log.Error("query due heartbeats failed", "err", err)
		return
	}
	for _, m := range due {
		period := int64(m.HeartbeatPeriod())
		grace := int64(m.HeartbeatGrace())
		late := now - (m.NextDeadlineAt - period - grace)
		reason := fmt.Sprintf("no heartbeat received for %s", formatAge(late))
		res := store.CheckResult{
			MonitorID: m.ID,
			OK:        false,
			Error:     reason,
			CreatedAt: now,
		}
		if err := s.store.RecordResult(&res); err != nil {
			s.log.Error("record heartbeat result failed", "monitor_id", m.ID, "err", err)
		}
		ev := s.tracker.Observe(&m, false, reason, now)
		s.notifyEvent(ev)
		m.LastCheckAt = now
		if err := s.store.SaveRuntime(&m); err != nil {
			s.log.Error("save heartbeat runtime failed", "monitor_id", m.ID, "err", err)
		}
	}
	stale, err := s.store.HeartbeatsStale(now, 60)
	if err != nil {
		s.log.Error("query stale heartbeats failed", "err", err)
		return
	}
	for _, m := range stale {
		res := store.CheckResult{
			MonitorID: m.ID,
			OK:        false,
			Error:     "still no heartbeat received",
			CreatedAt: now,
		}
		if err := s.store.RecordResult(&res); err != nil {
			s.log.Error("record stale heartbeat failed", "monitor_id", m.ID, "err", err)
		}
		m.LastCheckAt = now
		if err := s.store.SaveRuntime(&m); err != nil {
			s.log.Error("save stale runtime failed", "monitor_id", m.ID, "err", err)
		}
	}
}

func formatAge(sec int64) string {
	if sec < 0 {
		sec = 0
	}
	if sec < 90 {
		return fmt.Sprintf("%ds", sec)
	}
	return fmt.Sprintf("%dm", (sec+30)/60)
}

func (s *Scheduler) HandlePing(token string) error {
	m, err := s.store.MonitorByToken(token)
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	period := int64(m.HeartbeatPeriod())
	grace := int64(m.HeartbeatGrace())
	if period <= 0 {
		return fmt.Errorf("monitor %d has invalid heartbeat config", m.ID)
	}
	m.LastPingAt = now
	m.LastCheckAt = now
	m.NextDeadlineAt = now + period + grace
	if err := s.store.RecordResult(&store.CheckResult{
		MonitorID: m.ID,
		OK:        true,
		CreatedAt: now,
	}); err != nil {
		return fmt.Errorf("record ping result: %w", err)
	}
	ev := s.tracker.ForceRecover(m, now)
	if ev != nil {
		s.notifyEvent(ev)
	}
	return s.store.SaveRuntime(m)
}

var sslThresholds = []int{30, 14, 7}

func (s *Scheduler) sslLoop(ctx context.Context) {
	defer s.wg.Done()
	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sweepSSL()
		}
	}
}

func (s *Scheduler) sweepSSL() {
	targets, err := s.store.SSLAlertTargets()
	if err != nil {
		s.log.Error("query ssl targets failed", "err", err)
		return
	}
	now := time.Now().Unix()
	for _, m := range targets {
		daysLeft := int((m.SSLExpiresAt - now) / 86400)
		if daysLeft > sslThresholds[0] {
			if err := s.store.ClearSSLAlerts(m.ID, daysLeft); err != nil {
				s.log.Error("clear ssl alerts failed", "monitor_id", m.ID, "err", err)
			}
			continue
		}
		if daysLeft < 0 {
			daysLeft = 0
		}
		for _, threshold := range sslThresholds {
			if daysLeft > threshold {
				continue
			}
			alerted, err := s.store.SSLAlerted(m.ID, threshold)
			if err != nil {
				s.log.Error("check ssl alert failed", "monitor_id", m.ID, "err", err)
				continue
			}
			if alerted {
				continue
			}
			if err := s.store.MarkSSLAlert(m.ID, threshold, now); err != nil {
				s.log.Error("mark ssl alert failed", "monitor_id", m.ID, "err", err)
				continue
			}
			s.alerts.NotifySSL(m, daysLeft, m.SSLExpiresAt)
		}
	}
}
