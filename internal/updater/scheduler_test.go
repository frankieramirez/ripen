package updater

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/frankieramirez/ripen/internal/backend"
	"github.com/frankieramirez/ripen/internal/config"
	"github.com/frankieramirez/ripen/internal/domain"
	"github.com/frankieramirez/ripen/internal/event"
	"github.com/frankieramirez/ripen/internal/registry"
	"github.com/frankieramirez/ripen/internal/state"
)

type scheduleClock struct{ nanos atomic.Int64 }

func (c *scheduleClock) Now() time.Time        { return time.Unix(0, c.nanos.Load()).UTC() }
func (c *scheduleClock) Sleep(d time.Duration) { c.nanos.Add(int64(d)) }

type scheduleEvent struct {
	name    event.Name
	subject event.Subject
	data    event.Data
}
type scheduleSink chan scheduleEvent

func (s scheduleSink) Emit(n event.Name, subject event.Subject, data event.Data) {
	s <- scheduleEvent{n, subject, data}
}

type scheduleRegistry struct{}

func (scheduleRegistry) ResolveDigest(domain.ImageReference) (string, error) { return newDigest, nil }
func (scheduleRegistry) ResolvePlatformDigest(domain.ImageReference, registry.Platform) (string, error) {
	return newDigest, nil
}

type scheduleHealth struct{}

func (scheduleHealth) Check(config.HealthPolicy) (bool, error) { return true, nil }

type scheduleBackend struct {
	mu          sync.Mutex
	reads       map[string]int
	deployed    map[string]bool
	deployments []string
	started     chan string
	gate        <-chan struct{}
	deployGate  <-chan struct{}
	delay       time.Duration
	stackGates  map[string]<-chan struct{}
	transform   func(backend.StackState) backend.StackState
	active      atomic.Int32
	peak        atomic.Int32
	requests    atomic.Int64
}
type schedulePort struct {
	*scheduleBackend
	ctx context.Context
}

func (b *scheduleBackend) WithContext(ctx context.Context) backend.Port { return &schedulePort{b, ctx} }
func (b *scheduleBackend) Preflight() error                             { return nil }
func (b *scheduleBackend) Observe(s config.StackPolicy) (backend.StackState, error) {
	return b.WithContext(context.Background()).Observe(s)
}
func (b *scheduleBackend) RunningDigests(backend.StackState) (map[string]string, error) {
	return nil, nil
}
func (b *scheduleBackend) ServicesRunning(backend.StackState) (bool, string, error) {
	return true, "", nil
}
func (b *scheduleBackend) Deploy(s backend.StackState, _ string, _ bool) error {
	b.mu.Lock()
	b.deployments = append(b.deployments, s.Stack)
	b.mu.Unlock()
	if b.deployGate != nil {
		<-b.deployGate
	}
	b.mu.Lock()
	b.deployed[s.Stack] = true
	b.mu.Unlock()
	return nil
}
func (b *schedulePort) Observe(s config.StackPolicy) (backend.StackState, error) {
	b.requests.Add(1)
	n := b.active.Add(1)
	defer b.active.Add(-1)
	for old := b.peak.Load(); n > old; old = b.peak.Load() {
		if b.peak.CompareAndSwap(old, n) {
			break
		}
	}
	if b.started != nil {
		b.started <- s.Name
	}
	gate := b.gate
	if specific := b.stackGates[s.Name]; specific != nil {
		gate = specific
	}
	if gate != nil {
		select {
		case <-gate:
		case <-b.ctx.Done():
			return backend.StackState{}, b.ctx.Err()
		}
	}
	if b.delay > 0 {
		timer := time.NewTimer(b.delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-b.ctx.Done():
			return backend.StackState{}, b.ctx.Err()
		}
	}
	b.mu.Lock()
	b.reads[s.Name]++
	updated := b.deployed[s.Name]
	b.mu.Unlock()
	status := "outdated"
	if updated {
		status = "updated"
	}
	observed := backend.StackState{Backend: s.Backend, Stack: s.Name, Compose: singleCompose, Fingerprint: "stable", Services: []string{"web"}, ServiceImages: map[string]string{"web": webImage}, DeclaredImages: map[string]string{"web": webImage}, ImageStatus: status}
	if b.transform != nil {
		observed = b.transform(observed)
	}
	return observed, nil
}
func (b *scheduleBackend) count(name string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.reads[name]
}

func scheduleFixture(t testing.TB, n, concurrency int) (*Updater, *scheduleBackend, *scheduleClock, scheduleSink) {
	t.Helper()
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	clock := &scheduleClock{}
	clock.nanos.Store(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC).UnixNano())
	port := &scheduleBackend{reads: map[string]int{}, deployed: map[string]bool{}}
	stacks := make([]config.StackPolicy, n)
	for i := range stacks {
		stacks[i] = singleStack(fmt.Sprintf("stack-%02d", i), domain.BackendPortainer)
		key := state.Key{Backend: domain.BackendPortainer, Stack: stacks[i].Name}
		if err := store.SetAcceptedDigest(key, baseDigest, clock.Now()); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ObserveCandidate(key, newDigest, clock.Now().Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	policy := policyFor(stacks...)
	policy.ObservationConcurrency = concurrency
	policy.CandidateMinAgeSeconds = 1
	sink := make(scheduleSink, 10000)
	u, err := New(Options{Policy: policy, Backends: map[domain.Backend]backend.Port{domain.BackendPortainer: port}, Registry: scheduleRegistry{}, Health: scheduleHealth{}, State: store, Clock: clock, Events: sink, Actor: domain.ActorDaemon})
	if err != nil {
		t.Fatal(err)
	}
	return u, port, clock, sink
}
func waitScheduleEvent(t testing.TB, sink scheduleSink, name event.Name, mode string) scheduleEvent {
	t.Helper()
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case e := <-sink:
			if e.name == name && (mode == "" || e.data.Mode == mode) {
				return e
			}
		case <-timeout.C:
			t.Fatalf("timed out waiting for %s/%s", name, mode)
		}
	}
}
func startSchedule(t *testing.T, u *Updater, mode domain.Mode) (chan time.Time, context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ticks := make(chan time.Time)
	done := make(chan error, 1)
	go func() { done <- u.schedule(ctx, mode, time.Minute, ticks) }()
	t.Cleanup(cancel)
	return ticks, cancel, done
}
func finishSchedule(t *testing.T, cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("scheduler failed to drain")
	}
}

func TestLongDeploymentAllowsRepeatedUnrelatedObservationsAndExcludesItsWholeStack(t *testing.T) {
	u, port, clock, sink := scheduleFixture(t, 2, 2)
	gate := make(chan struct{})
	port.deployGate = gate
	ticks, cancel, done := startSchedule(t, u, domain.ModeApply)
	started, observed := false, false
	for !started || !observed {
		select {
		case e := <-sink:
			started = started || e.name == event.TransactionStarted
			observed = observed || (e.name == event.RunFinished && e.data.Mode == "monitor")
		case <-time.After(5 * time.Second):
			t.Fatal("initial cycle did not start deployment and finish observation")
		}
	}
	first := port.count("stack-00")
	for range 2 {
		clock.Sleep(time.Minute)
		ticks <- clock.Now()
		waitScheduleEvent(t, sink, event.RunFinished, "monitor")
	}
	if got := port.count("stack-00"); got != first {
		t.Fatalf("active stack observations changed from %d to %d", first, got)
	}
	if got := port.count("stack-01"); got != 3 {
		t.Fatalf("unrelated observations=%d want 3", got)
	}
	candidate, err := u.state.Candidate(state.Key{Backend: domain.BackendPortainer, Stack: "stack-00"})
	if err != nil || candidate == nil || candidate.Count != 2 {
		t.Fatalf("apply refresh inflated observations: candidate=%+v err=%v", candidate, err)
	}
	cancel()
	close(gate)
	finishSchedule(t, cancel, done)
}

func TestObservationWorkersAreBoundedAndMissedTicksCoalesce(t *testing.T) {
	u, port, clock, sink := scheduleFixture(t, 2, 2)
	gate := make(chan struct{})
	port.gate = gate
	port.started = make(chan string, 20)
	ticks, cancel, done := startSchedule(t, u, domain.ModeMonitor)
	for range 2 {
		select {
		case <-port.started:
		case <-time.After(5 * time.Second):
			t.Fatal("workers did not start")
		}
	}
	for range 4 {
		clock.Sleep(time.Minute)
		ticks <- clock.Now()
		waitScheduleEvent(t, sink, event.RunFinished, "monitor")
	}
	close(gate)
	for range 3 {
		waitScheduleEvent(t, sink, event.RunFinished, "monitor")
	}
	finishSchedule(t, cancel, done)
	for _, name := range []string{"stack-00", "stack-01"} {
		if got := port.count(name); got != 2 {
			t.Fatalf("%s reads=%d want two coalesced observations", name, got)
		}
	}
	if port.peak.Load() > 2 {
		t.Fatalf("peak=%d", port.peak.Load())
	}
}

func TestSchedulerCancellationDrainsBlockedObservationWorkers(t *testing.T) {
	u, port, _, _ := scheduleFixture(t, 4, 2)
	port.gate = make(chan struct{})
	port.started = make(chan string, 4)
	_, cancel, done := startSchedule(t, u, domain.ModeMonitor)
	for range 2 {
		select {
		case <-port.started:
		case <-time.After(5 * time.Second):
			t.Fatal("workers did not start")
		}
	}
	finishSchedule(t, cancel, done)
	if port.active.Load() != 0 {
		t.Fatal("observation workers remain active")
	}
	status, err := u.state.Status(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if status.LeaseActive {
		t.Fatal("lease remained active after drain")
	}
}

func TestDeploymentAdmissionPreservesPolicyOrderAndWaitsForCooldown(t *testing.T) {
	u, port, clock, sink := scheduleFixture(t, 2, 2)
	ticks, cancel, done := startSchedule(t, u, domain.ModeApply)
	first := waitScheduleEvent(t, sink, event.TransactionStarted, "")
	if first.subject.Stack != "stack-00" {
		t.Fatalf("first=%s", first.subject.Stack)
	}
	waitScheduleEvent(t, sink, event.RunFinished, "apply")
	clock.Sleep(30 * time.Second)
	ticks <- clock.Now()
	waitScheduleEvent(t, sink, event.RunFinished, "monitor")
	port.mu.Lock()
	count := len(port.deployments)
	port.mu.Unlock()
	if count != 1 {
		t.Fatalf("deployments before cooldown=%d", count)
	}
	clock.Sleep(31 * time.Second)
	ticks <- clock.Now()
	second := waitScheduleEvent(t, sink, event.TransactionStarted, "")
	if second.subject.Stack != "stack-01" {
		t.Fatalf("second=%s", second.subject.Stack)
	}
	waitScheduleEvent(t, sink, event.RunFinished, "apply")
	finishSchedule(t, cancel, done)
}

func BenchmarkObservationPass(b *testing.B) {
	for _, n := range []int{4, 16, 64} {
		for _, workers := range []int{1, 2} {
			b.Run(fmt.Sprintf("stacks=%d/concurrency=%d", n, workers), func(b *testing.B) {
				u, port, _, sink := scheduleFixture(b, n, workers)
				port.delay = time.Millisecond
				b.ResetTimer()
				for range b.N {
					ctx, cancel := context.WithCancel(context.Background())
					done := make(chan error, 1)
					go func() { done <- u.schedule(ctx, domain.ModeMonitor, time.Minute, nil) }()
					waitScheduleEvent(b, sink, event.RunFinished, "monitor")
					cancel()
					if err := <-done; err != nil {
						b.Fatal(err)
					}
				}
				b.StopTimer()
				b.ReportMetric(float64(port.requests.Load())/float64(b.N), "requests/pass")
				b.ReportMetric(float64(port.peak.Load()), "peak-reads")
			})
		}
	}
}

func TestBreakerOpeningDuringObservationPreventsSubsequentDeployment(t *testing.T) {
	u, port, clock, sink := scheduleFixture(t, 2, 2)
	gate := make(chan struct{})
	port.stackGates = map[string]<-chan struct{}{"stack-01": gate}
	port.started = make(chan string, 20)
	ticks, cancel, done := startSchedule(t, u, domain.ModeApply)
	waitScheduleEvent(t, sink, event.TransactionStarted, "")
	waitScheduleEvent(t, sink, event.RunFinished, "apply")
	if err := u.state.OpenBreaker("unrelated observation found unresolved state", clock.Now()); err != nil {
		t.Fatal(err)
	}

	close(gate)
	waitScheduleEvent(t, sink, event.RunFinished, "monitor")
	clock.Sleep(time.Minute)
	ticks <- clock.Now()
	waitScheduleEvent(t, sink, event.RunFinished, "monitor")
	finishSchedule(t, cancel, done)

	port.mu.Lock()
	defer port.mu.Unlock()
	if len(port.deployments) != 1 {
		t.Fatalf("deployments=%v", port.deployments)
	}
}

func TestFiniteChecksRefreshDriftAndIneligibilityWithoutChangingCandidateHistory(t *testing.T) {
	for _, outcome := range []domain.ResultCode{domain.ResultDrifted, domain.ResultIneligible} {
		t.Run(string(outcome), func(t *testing.T) {
			u, port, clock, _ := scheduleFixture(t, 1, 2)
			if outcome == domain.ResultIneligible {
				port.transform = func(s backend.StackState) backend.StackState { s.Services = []string{"unexpected"}; return s }
			} else {
				port.transform = func(s backend.StackState) backend.StackState {
					s.RunningDigests = map[string]string{"web": sidecarDigest}
					return s
				}
			}
			key := state.Key{Backend: domain.BackendPortainer, Stack: "stack-00"}
			before, err := u.state.Candidate(key)
			if err != nil {
				t.Fatal(err)
			}

			for range 2 {
				clock.Sleep(time.Minute)
				report, err := u.RunContext(context.Background(), domain.ModeMonitor)
				if err != nil {
					t.Fatal(err)
				}
				if len(report.Results) != 1 || report.Results[0].Code != outcome {
					t.Fatalf("results=%+v", report.Results)
				}
				evaluations, err := u.state.Evaluations()
				if err != nil {
					t.Fatal(err)
				}
				if len(evaluations) != 1 || evaluations[0].Result != outcome || !evaluations[0].EvaluatedAt.Equal(clock.Now()) {
					t.Fatalf("evaluations=%+v", evaluations)
				}
				checks, err := u.state.StackChecks()
				if err != nil {
					t.Fatal(err)
				}
				if len(checks) != 1 || checks[0].CompletedAt == nil || !checks[0].CompletedAt.Equal(clock.Now()) {
					t.Fatalf("checks=%+v", checks)
				}
			}

			after, err := u.state.Candidate(key)
			if err != nil {
				t.Fatal(err)
			}
			if before == nil || after == nil || before.Count != after.Count || !before.LastSeen.Equal(after.LastSeen) {
				t.Fatalf("candidate changed: before=%+v after=%+v", before, after)
			}
		})
	}
}

type renewalClock struct {
	*scheduleClock
	ticks     chan time.Time
	intervals chan time.Duration
}

func (c *renewalClock) Ticker(interval time.Duration) (<-chan time.Time, func()) {
	c.intervals <- interval
	return c.ticks, func() {}
}

func TestLeaseRenewalExtendsOwnershipBeyondTheOriginalExpiry(t *testing.T) {
	u, _, clock, _ := scheduleFixture(t, 1, 1)
	manual := &renewalClock{scheduleClock: clock, ticks: make(chan time.Time), intervals: make(chan time.Duration, 1)}
	u.clock = manual
	token, acquired, err := u.state.AcquireLease(clock.Now(), u.policy.LeaseTTLSeconds)
	if err != nil || !acquired {
		t.Fatalf("acquired=%v err=%v", acquired, err)
	}
	owner, release := u.owned(context.Background(), token)
	defer release()
	interval := <-manual.intervals
	if interval != 600*time.Second {
		t.Fatalf("renewal interval=%v", interval)
	}

	clock.Sleep(interval)
	manual.ticks <- clock.Now()
	manual.ticks <- clock.Now()
	clock.Sleep(1201 * time.Second)

	if err := owner.checkOwnership(); err != nil {
		t.Fatalf("renewed owner expired at original deadline: %v", err)
	}
}

func TestLeaseLossCancelsObservationAndStopsQueuedAdmissions(t *testing.T) {
	u, port, clock, _ := scheduleFixture(t, 4, 2)
	manual := &renewalClock{scheduleClock: clock, ticks: make(chan time.Time), intervals: make(chan time.Duration, 1)}
	u.clock = manual
	port.gate = make(chan struct{})
	port.started = make(chan string, 4)
	_, cancel, done := startSchedule(t, u, domain.ModeMonitor)
	<-manual.intervals
	for range 2 {
		select {
		case <-port.started:
		case <-time.After(5 * time.Second):
			t.Fatal("workers did not start")
		}
	}
	clock.Sleep(1801 * time.Second)
	replacement, acquired, err := u.state.AcquireLease(clock.Now(), 1800)
	if err != nil || !acquired {
		t.Fatalf("replacement acquired=%v err=%v", acquired, err)
	}
	defer func() { _ = u.state.ReleaseLease(replacement) }()

	manual.ticks <- clock.Now()

	select {
	case err := <-done:
		if !errors.Is(err, state.ErrLeaseLost) {
			t.Fatalf("err=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("lost ownership did not drain workers")
	}
	cancel()
	if port.active.Load() != 0 || port.requests.Load() != 2 {
		t.Fatalf("active=%d requests=%d", port.active.Load(), port.requests.Load())
	}
	if err := u.state.CheckLease(replacement, clock.Now()); err != nil {
		t.Fatalf("old owner's cleanup removed replacement lease: %v", err)
	}
}
