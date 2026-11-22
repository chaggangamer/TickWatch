package incident

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/chaggangamer/tickwatch/internal/store"
)

func testTracker(t *testing.T) (*Tracker, store.Store, store.Monitor) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	m := &store.Monitor{
		Type: store.TypeHTTP, Name: "web",
		Config:   []byte(`{"url":"https://example.com","method":"GET"}`),
		Interval: 60, Timeout: 10, FailureThreshold: 3, Cooldown: 300, Enabled: true,
		Status: store.StatusUp, CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix(),
		NextRunAt: time.Now().Unix(),
	}
	if err := s.CreateMonitor(m); err != nil {
		t.Fatal(err)
	}
	tr := New(s, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return tr, s, *m
}

func monitor(name string, threshold int) store.Monitor {
	return store.Monitor{ID: 1, Type: store.TypeHTTP, Name: name, FailureThreshold: threshold, Status: store.StatusUp}
}

func TestThresholdTransitions(t *testing.T) {
	tr, s, _ := testTracker(t)
	m := monitor("web", 3)
	now := time.Now().Unix()

	if ev := tr.Observe(&m, false, "refused", now); ev != nil {
		t.Errorf("fail 1 should not trigger: %+v", ev)
	}
	if m.Status != store.StatusUp {
		t.Errorf("status after fail 1 = %s", m.Status)
	}
	if ev := tr.Observe(&m, false, "refused", now+10); ev != nil {
		t.Errorf("fail 2 should not trigger: %+v", ev)
	}
	if open, _ := s.OpenIncidents(); len(open) != 0 {
		t.Errorf("no incident should exist yet, got %d", len(open))
	}

	ev := tr.Observe(&m, false, "connection refused", now+20)
	if ev == nil || ev.Kind != EventDown {
		t.Fatalf("fail 3 should trigger down, got %+v", ev)
	}
	if ev.Reason != "connection refused" {
		t.Errorf("reason = %q", ev.Reason)
	}
	m.Status = store.StatusUp
	open, _ := s.OpenIncidents()
	if len(open) != 1 {
		t.Fatalf("open incidents = %d, want 1", len(open))
	}
}

func TestRecovery(t *testing.T) {
	tr, s, _ := testTracker(t)
	m := monitor("web", 1)
	now := time.Now().Unix()

	if ev := tr.Observe(&m, false, "refused", now); ev == nil {
		t.Fatal("threshold 1 should trigger immediately")
	}
	if err := s.SaveRuntime(&m); err != nil {
		t.Fatal(err)
	}
	tr.Observe(&m, false, "refused", now+10)

	ev := tr.Observe(&m, true, "", now+40)
	if ev == nil || ev.Kind != EventRecovered {
		t.Fatalf("ok should recover, got %+v", ev)
	}
	if ev.Downtime != 40 {
		t.Errorf("downtime = %d, want 40", ev.Downtime)
	}
	if m.Status != store.StatusUp {
		t.Errorf("status after recovery = %s", m.Status)
	}
	if open, _ := s.OpenIncidents(); len(open) != 0 {
		t.Errorf("incident should be closed, got %d", len(open))
	}

	if ev := tr.Observe(&m, true, "", now+50); ev != nil {
		t.Errorf("repeated ok should not trigger: %+v", ev)
	}
}

func TestForceRecover(t *testing.T) {
	tr, s, _ := testTracker(t)
	m := monitor("hb", 1)
	now := time.Now().Unix()

	if ev := tr.ForceRecover(&m, now); ev != nil {
		t.Errorf("force recover on up monitor should return nil, got %+v", ev)
	}
	tr.Observe(&m, false, "missed", now)
	if err := s.SaveRuntime(&m); err != nil {
		t.Fatal(err)
	}
	ev := tr.ForceRecover(&m, now+30)
	if ev == nil || ev.Kind != EventRecovered {
		t.Fatalf("force recover should recover, got %+v", ev)
	}
	if ev.Downtime != 30 {
		t.Errorf("downtime = %d, want 30", ev.Downtime)
	}
}

func TestLoad(t *testing.T) {
	tr, s, _ := testTracker(t)
	m := monitor("web", 3)
	now := time.Now().Unix()

	if _, err := s.OpenIncident(m.ID, now-600, "refused"); err != nil {
		t.Fatal(err)
	}
	down := m
	down.Status = store.StatusDown
	if err := s.SaveRuntime(&down); err != nil {
		t.Fatal(err)
	}

	tr.Load([]store.Monitor{down}, mustOpen(s))

	if !tr.IsDown(m.ID) {
		t.Error("down monitor should be loaded as down")
	}

	ev := tr.Observe(&down, true, "", now)
	if ev == nil || ev.Kind != EventRecovered {
		t.Fatalf("first ok after load should recover, got %+v", ev)
	}
	if ev.Downtime != 600 {
		t.Errorf("downtime from loaded incident = %d, want 600", ev.Downtime)
	}
}

func mustOpen(s store.Store) []store.Incident {
	open, err := s.OpenIncidents()
	if err != nil {
		panic(err)
	}
	return open
}

func TestReset(t *testing.T) {
	tr, _, _ := testTracker(t)
	m := monitor("web", 3)
	now := time.Now().Unix()
	tr.Observe(&m, false, "a", now)
	tr.Observe(&m, false, "b", now)
	tr.Reset(m.ID)
	if ev := tr.Observe(&m, false, "c", now); ev != nil {
		t.Error("counter should reset after Reset")
	}
	if tr.IsDown(m.ID) {
		t.Error("monitor should not be down after Reset")
	}
}

func TestUnknownMonitorFirstSeen(t *testing.T) {
	tr, _, _ := testTracker(t)
	m := monitor("fresh", 1)
	now := time.Now().Unix()
	ev := tr.Observe(&m, false, "refused", now)
	if ev == nil || ev.Kind != EventDown {
		t.Fatalf("unknown monitor with threshold 1 should trigger: %+v", ev)
	}
}

func TestResetKeepsDownStateFromStore(t *testing.T) {
	tr, s, m := testTracker(t)
	m.FailureThreshold = 1
	now := time.Now().Unix()

	if ev := tr.Observe(&m, false, "refused", now); ev == nil {
		t.Fatal("expected down event")
	}
	if err := s.SaveRuntime(&m); err != nil {
		t.Fatal(err)
	}
	if !tr.IsDown(m.ID) {
		t.Fatal("monitor should be down")
	}

	tr.Reset(m.ID)

	if !tr.IsDown(m.ID) {
		t.Error("after reset the monitor is still down in the store, tracker should know")
	}
	ev := tr.Observe(&m, true, "", now+30)
	if ev == nil || ev.Kind != EventRecovered {
		t.Fatalf("success after reset should recover, got %+v", ev)
	}
	if m.Status != store.StatusUp {
		t.Errorf("status = %s, want up", m.Status)
	}
	if open, _ := s.OpenIncidents(); len(open) != 0 {
		t.Errorf("incident should be closed, got %d open", len(open))
	}
}

func TestResetUpMonitorStaysUp(t *testing.T) {
	tr, _, m := testTracker(t)
	m.FailureThreshold = 3
	tr.Observe(&m, false, "refused", time.Now().Unix())
	tr.Reset(m.ID)
	if tr.IsDown(m.ID) {
		t.Error("monitor was never down, reset should keep it up")
	}
	if ev := tr.Observe(&m, false, "refused", time.Now().Unix()+10); ev != nil {
		t.Errorf("one failure under threshold 3 should not trip: %+v", ev)
	}
}
