package updater

import (
	"maps"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/frankieramirez/ripen/internal/backend"
	"github.com/frankieramirez/ripen/internal/composefile"
	"github.com/frankieramirez/ripen/internal/config"
	"github.com/frankieramirez/ripen/internal/domain"
	"github.com/frankieramirez/ripen/internal/event"
	"github.com/frankieramirez/ripen/internal/proposal"
	"github.com/frankieramirez/ripen/internal/registry"
	"github.com/frankieramirez/ripen/internal/state"
)

var (
	baseDigest    = "sha256:" + strings.Repeat("1", 64)
	newDigest     = "sha256:" + strings.Repeat("2", 64)
	sidecarDigest = "sha256:" + strings.Repeat("3", 64)
	thirdDigest   = "sha256:" + strings.Repeat("4", 64)
)

const (
	webImage     = "ghcr.io/example/web:1.4.0"
	sidecarImage = "ghcr.io/example/sidecar:0.9.1"

	singleCompose = "services:\n  web:\n    image: " + webImage + "\n"
	multiCompose  = "services:\n  web:\n    image: " + webImage +
		"\n  sidecar:\n    image: " + sidecarImage + "\n"
)

// --- fakes ---

type deployment struct {
	compose string
	repull  bool
}

// fakeBackend is an engine that remembers: it deploys by replacing the
// document it holds, and re-reads running digests out of that document's
// pins, the way a real engine's running containers would follow it.
type fakeBackend struct {
	name         domain.Backend
	compose      string
	running      map[string]string
	imageStatus  string
	resolved     map[string]string
	gitBacked    bool
	servicesUp   bool
	preflightErr error
	observeErr   error
	deployErr    func(attempt int) error
	deployLands  bool
	onObserve    func(f *fakeBackend, observation int)

	observations int
	deployments  []deployment
}

func newBackend(name domain.Backend, compose string) *fakeBackend {
	return &fakeBackend{name: name, compose: compose, servicesUp: true}
}

func (f *fakeBackend) Preflight() error { return f.preflightErr }

func (f *fakeBackend) Observe(stack config.StackPolicy) (backend.StackState, error) {
	f.observations++
	if f.onObserve != nil {
		f.onObserve(f, f.observations)
	}
	if f.observeErr != nil {
		return backend.StackState{}, f.observeErr
	}
	services, err := composefile.Services(f.compose)
	if err != nil {
		return backend.StackState{}, backend.Ineligible("%v", err)
	}
	images := map[string]string{}
	for _, name := range services {
		if image, err := composefile.ServiceImage(f.compose, name); err == nil {
			images[name] = image
		}
	}
	observed := backend.StackState{
		Backend:        f.name,
		Stack:          stack.Name,
		Compose:        f.compose,
		Fingerprint:    fingerprintOf(f.compose),
		Services:       services,
		ServiceImages:  images,
		DeclaredImages: images,
		ImageStatus:    f.imageStatus,
		GitBacked:      f.gitBacked,
		Handle:         stack.Name,
	}
	if f.resolved != nil {
		// An interpolated image line reads one way and runs another.
		observed.ServiceImages = maps.Clone(f.resolved)
	}
	if f.running != nil {
		observed.RunningDigests = maps.Clone(f.running)
	}
	return observed, nil
}

func (f *fakeBackend) RunningDigests(backend.StackState) (map[string]string, error) {
	if f.running == nil {
		return nil, nil
	}
	return maps.Clone(f.running), nil
}

func (f *fakeBackend) Deploy(_ backend.StackState, compose string, repull bool) error {
	f.deployments = append(f.deployments, deployment{compose: compose, repull: repull})
	var failure error
	if f.deployErr != nil {
		failure = f.deployErr(len(f.deployments))
	}
	// deployLands is the ambiguous case: the call failed but the
	// deployment happened anyway, which is what a timeout looks like.
	if failure != nil && !f.deployLands {
		return failure
	}
	f.compose = compose
	for service := range f.running {
		image, err := composefile.ServiceImage(compose, service)
		if err != nil {
			continue
		}
		if _, pin, pinned := strings.Cut(image, "@"); pinned {
			f.running[service] = pin
		}
	}
	if repull {
		f.imageStatus = "updated"
	}
	return failure
}

func (f *fakeBackend) ServicesRunning(backend.StackState) (bool, string, error) {
	if f.servicesUp {
		return true, "", nil
	}
	return false, "a service is not running", nil
}

func (f *fakeBackend) lastDeployment() deployment {
	if len(f.deployments) == 0 {
		return deployment{}
	}
	return f.deployments[len(f.deployments)-1]
}

func fingerprintOf(compose string) string {
	digest := domain.NewFingerprint()
	digest.Add("compose", compose)
	return digest.Sum()
}

type fakeRegistry struct {
	digests map[string]string
	err     error
	lookups []string
}

func (f *fakeRegistry) resolve(image domain.ImageReference) (string, error) {
	f.lookups = append(f.lookups, image.Tagged())
	if f.err != nil {
		return "", f.err
	}
	if digest, ok := f.digests[image.Tagged()]; ok {
		return digest, nil
	}
	return "", nil
}

func (f *fakeRegistry) ResolveDigest(image domain.ImageReference) (string, error) {
	return f.resolve(image)
}

func (f *fakeRegistry) ResolvePlatformDigest(image domain.ImageReference, _ registry.Platform) (string, error) {
	return f.resolve(image)
}

type fakeHealth struct {
	answer func(policy config.HealthPolicy, call int) (bool, error)
	checks []config.HealthPolicy
}

func (f *fakeHealth) Check(policy config.HealthPolicy) (bool, error) {
	f.checks = append(f.checks, policy)
	if f.answer == nil {
		return true, nil
	}
	return f.answer(policy, len(f.checks))
}

func (f *fakeHealth) checksFor(target string) int {
	count := 0
	for _, policy := range f.checks {
		if policy.Target == target {
			count++
		}
	}
	return count
}

type fakeProposals struct {
	changes []proposal.Change
	result  proposal.Result
	err     error
}

func (f *fakeProposals) Propose(change proposal.Change) (proposal.Result, error) {
	f.changes = append(f.changes, change)
	if f.err != nil {
		return proposal.Result{}, f.err
	}
	return f.result, nil
}

type recordedEvent struct {
	name    event.Name
	subject event.Subject
	data    event.Data
}

type recordingSink struct {
	events []recordedEvent
	panics bool
	onEmit func(recorded recordedEvent)
}

func (s *recordingSink) Emit(name event.Name, subject event.Subject, data event.Data) {
	if s.panics {
		panic("the notifier fell over")
	}
	recorded := recordedEvent{name: name, subject: subject, data: data}
	s.events = append(s.events, recorded)
	if s.onEmit != nil {
		s.onEmit(recorded)
	}
}

func (s *recordingSink) saw(name event.Name) bool {
	for _, recorded := range s.events {
		if recorded.name == name {
			return true
		}
	}
	return false
}

type fakeClock struct {
	now time.Time
}

func (c *fakeClock) Now() time.Time { return c.now }

// Sleep moves the fake clock instead of blocking, so verification loops
// reach their deadline in a test without waiting for it.
func (c *fakeClock) Sleep(duration time.Duration) { c.now = c.now.Add(duration) }

// --- policies ---

func healthAt(target string) config.HealthPolicy {
	return config.HealthPolicy{Type: "http", Target: target, AcceptedStatus: []int{200}, TimeoutSeconds: 5}
}

func singleStack(name string, backendName domain.Backend) config.StackPolicy {
	health := healthAt("http://media:8080/health")
	return config.StackPolicy{
		Name:             name,
		Backend:          backendName,
		Enabled:          true,
		AutoApply:        true,
		ExpectedServices: []string{"web"},
		Health:           &health,
	}
}

func multiStack() config.StackPolicy {
	return config.StackPolicy{
		Name:             "media",
		Backend:          domain.BackendDockerCompose,
		Enabled:          true,
		ExpectedServices: []string{"web", "sidecar"},
		Services: []config.ServicePolicy{
			{Name: "web", Enabled: true, AutoApply: true, Health: healthAt("http://media:8080/health")},
			{Name: "sidecar", Enabled: true, AutoApply: true, Health: healthAt("http://media:9090/health")},
		},
	}
}

func policyFor(stacks ...config.StackPolicy) *config.Policy {
	return &config.Policy{
		Mode:                       domain.ModeMonitor,
		MaxUpdatesPerRun:           1,
		VerificationTimeoutSeconds: 30,
		CandidateMinAgeSeconds:     86400,
		LeaseTTLSeconds:            1800,
		CheckIntervalSeconds:       86400,
		Stacks:                     stacks,
	}
}

// --- harness ---

type harness struct {
	t         *testing.T
	policy    *config.Policy
	store     *state.Store
	backends  map[domain.Backend]*fakeBackend
	registry  *fakeRegistry
	health    *fakeHealth
	proposals *fakeProposals
	events    *recordingSink
	clock     *fakeClock
	updater   *Updater
}

func newHarness(t *testing.T, policy *config.Policy, backends map[domain.Backend]*fakeBackend) *harness {
	t.Helper()
	store, err := state.Open(filepath.Join(t.TempDir(), "state", "ripen.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	harness := &harness{
		t:        t,
		policy:   policy,
		store:    store,
		backends: backends,
		registry: &fakeRegistry{digests: map[string]string{
			webImage:     baseDigest,
			sidecarImage: sidecarDigest,
		}},
		health:    &fakeHealth{},
		proposals: &fakeProposals{result: proposal.Result{URL: "https://github.com/x/y/pull/1", Created: true}},
		events:    &recordingSink{},
		clock:     &fakeClock{now: time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)},
	}
	ports := map[domain.Backend]backend.Port{}
	for name, port := range backends {
		ports[name] = port
	}
	harness.updater, err = New(Options{
		Policy:    policy,
		Backends:  ports,
		Registry:  harness.registry,
		Health:    harness.health,
		State:     store,
		Proposals: harness.proposals,
		Events:    harness.events,
		Clock:     harness.clock,
		Actor:     domain.ActorCLI,
	})
	if err != nil {
		t.Fatal(err)
	}
	return harness
}

// singleHarness is the common case: one stack on one backend.
func singleHarness(t *testing.T, stack config.StackPolicy, engine *fakeBackend) *harness {
	t.Helper()
	return newHarness(t, policyFor(stack), map[domain.Backend]*fakeBackend{stack.Backend: engine})
}

func (h *harness) run(mode domain.Mode) Report {
	h.t.Helper()
	report, err := h.updater.Run(mode)
	if err != nil {
		h.t.Fatalf("run failed: %v", err)
	}
	return report
}

// result finds one Service's result by its state key service name.
func (h *harness) result(report Report, service string) Result {
	h.t.Helper()
	for _, result := range report.Results {
		if result.Key.Service == service {
			return result
		}
	}
	h.t.Fatalf("no result for service %q in %+v", service, report.Results)
	return Result{}
}

func (h *harness) expect(report Report, service string, code domain.ResultCode) Result {
	h.t.Helper()
	result := h.result(report, service)
	if result.Code != code {
		h.t.Fatalf("result for %q = %s (%s), want %s", service, result.Code, result.Detail, code)
	}
	return result
}

func (h *harness) accepted(key state.Key) string {
	h.t.Helper()
	digest, found, err := h.store.AcceptedDigest(key)
	if err != nil {
		h.t.Fatal(err)
	}
	if !found {
		return ""
	}
	return digest
}

func (h *harness) status() state.Status {
	h.t.Helper()
	status, err := h.store.Status(h.clock.now)
	if err != nil {
		h.t.Fatal(err)
	}
	return status
}

// mature walks a Candidate through the maturity window: a second
// observation and enough elapsed time.
func (h *harness) mature() {
	h.t.Helper()
	h.clock.now = h.clock.now.Add(time.Duration(h.policy.CandidateMinAgeSeconds+1) * time.Second)
}

func key(backendName domain.Backend, service string) state.Key {
	return state.Key{Backend: backendName, Stack: "media", Service: service}
}
