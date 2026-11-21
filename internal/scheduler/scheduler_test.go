package scheduler

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/chaggangamer/tickwatch/internal/alert"
	"github.com/chaggangamer/tickwatch/internal/checker"
	"github.com/chaggangamer/tickwatch/internal/incident"
	"github.com/chaggangamer/tickwatch/internal/store"
)

type fakeRunner struct {
	mu    sync.Mutex
	fail  bool
	calls int
}

func (f *fakeRunner) Run(ctx context.Context, m store.Monitor) checker.Result {
	f.mu.Lock()
	f.calls++
	fail := f.fail
	f.mu.Unlock()
	if fail {
		return checker.Result{OK: false, Error: "synthetic failure"}
	}
	return checker.Result{OK: true, StatusCode: 200, LatencyMS: 12}
}

func (f *fakeRunner) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type testEnv struct {
	store   *store.DB
	tracker *incident.Tracker
	alerts  *alert.Dispatcher
	sched   *Scheduler
	cancel  context.CancelFunc
}

func newTestEnv(t *testing.T, workers int) *testEnv {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	tr := incident.New(s, log)
	al := alert.New(s, log)
	sched := New(Deps{
		Store:   s,
		Runner:  &fakeRunner{},
		Tracker: tr,
		Alerts:  al,
		Workers: workers,
		Log:     log,
	})
	return &testEnv{store: s.(*store.DB), tracker: tr, alerts: al, sched: sched}
}

func (e *testEnv) start() {
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	e.sched.sweepEvery = 100 * time.Millisecond
	go e.sched.Run(ctx)
}

func (e *testEnv) stop() {
	e.cancel()
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

func newHTTPMonitor(t *testing.T, s *store.DB, threshold int) *store.Monitor {
	t.Helper()
	m := &store.Monitor{
		Type: store.TypeHTTP, Name: "web",
		Config:   []byte(`{"url":"https://example.com","method":"GET"}`),
		Interval: 2, Timeout: 5, FailureThreshold: threshold, Cooldown: 60,
		Enabled: true, Status: store.StatusUp,
		CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix(),
		NextRunAt: time.Now().Unix(),
	}
	if err := s.CreateMonitor(m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestRunsDueChecks(t *testing.T) {
	env := newTestEnv(t, 2)
	m := newHTTPMonitor(t, env.store, 3)
	env.start()
	defer env.stop()

	waitFor(t, 8*time.Second, func() bool {
		results, _ := env.store.RecentResults(m.ID, 1)
		return len(results) == 1
	})
	got, _ := env.store.Monitor(m.ID)
	if got.Status != store.StatusUp {
		t.Errorf("status = %s, want up", got.Status)
	}
	if got.NextRunAt <= time.Now().Unix()-10 {
		t.Errorf("next run not rescheduled: %d", got.NextRunAt)
	}
}

func TestGoesDownAfterThreshold(t *testing.T) {
	env := newTestEnv(t, 2)
	m := newHTTPMonitor(t, env.store, 2)
	runner := env.sched.runner.(*fakeRunner)
	runner.fail = true

	env.start()
	defer env.stop()

	waitFor(t, 8*time.Second, func() bool {
		got, err := env.store.Monitor(m.ID)
		return err == nil && got.Status == store.StatusDown
	})
	open, _ := env.store.OpenIncidents()
	if len(open) != 1 {
		t.Fatalf("open incidents = %d, want 1", len(open))
	}

	runner.fail = false
	waitFor(t, 8*time.Second, func() bool {
		got, err := env.store.Monitor(m.ID)
		return err == nil && got.Status == store.StatusUp
	})
	open, _ = env.store.OpenIncidents()
	if len(open) != 0 {
		t.Errorf("incident should be closed, got %d", len(open))
	}
}

func TestHeartbeatMissedPing(t *testing.T) {
	env := newTestEnv(t, 1)
	m := &store.Monitor{
		Type: store.TypeHeartbeat, Name: "nightly",
		Config:   []byte(`{"period":60,"grace":0}`),
		Interval: 60, Timeout: 5, FailureThreshold: 3, Cooldown: 60,
		Enabled: true, Status: store.StatusUp,
		CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix(),
		NextRunAt: time.Now().Unix(),
	}
	store.Normalize(m)
	m.NextDeadlineAt = time.Now().Unix() - 1
	m.HeartbeatToken = store.NewToken(16)
	if err := env.store.CreateMonitor(m); err != nil {
		t.Fatal(err)
	}
	env.start()
	defer env.stop()

	waitFor(t, 8*time.Second, func() bool {
		got, err := env.store.Monitor(m.ID)
		return err == nil && got.Status == store.StatusDown
	})

	if err := env.sched.HandlePing(m.HeartbeatToken); err != nil {
		t.Fatalf("handle ping: %v", err)
	}
	got, _ := env.store.Monitor(m.ID)
	if got.Status != store.StatusUp {
		t.Errorf("status after ping = %s, want up", got.Status)
	}
	if got.LastPingAt == 0 {
		t.Error("last ping not recorded")
	}
	if open, _ := env.store.OpenIncidents(); len(open) != 0 {
		t.Errorf("incident should be closed after ping, got %d", len(open))
	}

	if err := env.sched.HandlePing("unknown-token"); err != store.ErrNotFound {
		t.Errorf("unknown token err = %v, want ErrNotFound", err)
	}
}

func TestStaggerOverdue(t *testing.T) {
	env := newTestEnv(t, 1)
	m := newHTTPMonitor(t, env.store, 3)
	m.NextRunAt = time.Now().Unix() - 100
	if err := env.store.SaveRuntime(m); err != nil {
		t.Fatal(err)
	}

	if err := env.sched.staggerOverdue(); err != nil {
		t.Fatalf("stagger: %v", err)
	}
	got, _ := env.store.Monitor(m.ID)
	now := time.Now().Unix()
	if got.NextRunAt < now || got.NextRunAt > now+31 {
		t.Errorf("next run = %d, want within [now, now+30]", got.NextRunAt)
	}
}

func TestJitterBounds(t *testing.T) {
	for interval := 20; interval <= 600; interval += 10 {
		j := jitterSeconds(interval)
		bound := interval / 20
		if bound < 1 {
			bound = 1
		}
		if j < -bound || j > bound {
			t.Fatalf("jitter(%d) = %d, want within [%d,%d]", interval, j, -bound, bound)
		}
	}
}

func TestCheckBudget(t *testing.T) {
	env := newTestEnv(t, 1)
	m := store.Monitor{Timeout: 10, Retries: 2}
	got := env.sched.checkBudget(m)
	want := 12*time.Second + 2*(10*time.Second+time.Second)
	if got != want {
		t.Errorf("budget = %v, want %v", got, want)
	}
}

func TestGracefulShutdown(t *testing.T) {
	env := newTestEnv(t, 4)
	newHTTPMonitor(t, env.store, 3)
	env.start()
	waitFor(t, 5*time.Second, func() bool {
		return env.sched.runner.(*fakeRunner).callCount() > 0
	})
	done := make(chan struct{})
	go func() {
		env.stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("scheduler did not shut down within 10s")
	}
}
