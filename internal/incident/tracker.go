package incident

import (
	"log/slog"
	"sync"

	"github.com/chaggangamer/tickwatch/internal/store"
)

const (
	EventDown      = "down"
	EventRecovered = "recovered"
)

type Event struct {
	Monitor    store.Monitor
	Kind       string
	Reason     string
	IncidentID int64
	Downtime   int64
}

type state struct {
	fails     int
	down      bool
	downSince int64
}

type Tracker struct {
	mu    sync.Mutex
	st    map[int64]*state
	store store.Store
	log   *slog.Logger
}

func New(s store.Store, log *slog.Logger) *Tracker {
	if log == nil {
		log = slog.Default()
	}
	return &Tracker{st: map[int64]*state{}, store: s, log: log}
}

func (t *Tracker) Load(monitors []store.Monitor, open []store.Incident) {
	t.mu.Lock()
	defer t.mu.Unlock()
	started := map[int64]int64{}
	for _, inc := range open {
		started[inc.MonitorID] = inc.StartedAt
	}
	for _, m := range monitors {
		if m.Status == store.StatusDown {
			t.st[m.ID] = &state{down: true, fails: m.FailureThreshold, downSince: started[m.ID]}
			continue
		}
		t.st[m.ID] = &state{}
	}
}

func (t *Tracker) Observe(m *store.Monitor, ok bool, reason string, now int64) *Event {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.state(m.ID)
	if ok {
		s.fails = 0
		if s.down {
			return t.recover(m, s, now)
		}
		return nil
	}
	s.fails++
	if !s.down && s.fails >= m.FailureThreshold {
		s.down = true
		s.downSince = now
		id, err := t.store.OpenIncident(m.ID, now, reason)
		if err != nil {
			t.log.Error("open incident failed", "monitor_id", m.ID, "err", err)
			id = 0
		}
		m.Status = store.StatusDown
		return &Event{Monitor: *m, Kind: EventDown, Reason: reason, IncidentID: id}
	}
	return nil
}

func (t *Tracker) ForceRecover(m *store.Monitor, now int64) *Event {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.state(m.ID)
	s.fails = 0
	if s.down {
		return t.recover(m, s, now)
	}
	return nil
}

func (t *Tracker) Reset(monitorID int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.st, monitorID)
}

func (t *Tracker) IsDown(monitorID int64) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.st[monitorID]
	return s != nil && s.down
}

func (t *Tracker) state(id int64) *state {
	s := t.st[id]
	if s == nil {
		s = &state{}
		t.st[id] = s
	}
	return s
}

func (t *Tracker) recover(m *store.Monitor, s *state, now int64) *Event {
	downtime := now - s.downSince
	if downtime < 0 {
		downtime = 0
	}
	s.down = false
	if err := t.store.CloseIncidents(m.ID, now); err != nil {
		t.log.Error("close incident failed", "monitor_id", m.ID, "err", err)
	}
	m.Status = store.StatusUp
	return &Event{Monitor: *m, Kind: EventRecovered, Downtime: downtime}
}
