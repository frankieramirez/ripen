package updater

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/frankieramirez/ripen/internal/config"
	"github.com/frankieramirez/ripen/internal/domain"
	"github.com/frankieramirez/ripen/internal/event"
	"github.com/frankieramirez/ripen/internal/state"
)

type observationPass struct {
	report    Report
	remaining int
	failure   error
}

type stackWork struct {
	index int
	pass  *observationPass
	apply bool
}

type stackCompletion struct {
	work    stackWork
	results []Result
	applied int
	err     error
}

type coordinator struct {
	unavailable   map[domain.Backend]string
	root          *Updater
	owner         *Updater
	release       func()
	busy          []bool
	running       []bool
	due           []bool
	queue         []stackWork
	observations  int
	applying      bool
	applyingIndex int
	results       chan stackCompletion
	latest        [][]Result
	cooldown      time.Time
	interval      time.Duration
	nextTick      time.Time
	progress      state.SchedulerProgress
	stopping      bool
	failure       error
}

// Schedule observes on fixed ticks while serializing outbound Transactions.
func (u *Updater) Schedule(ctx context.Context, mode domain.Mode, interval time.Duration) error {
	if interval <= 0 {
		return errors.New("observation interval must be positive")
	}
	ticks, stop := u.ticker(interval)
	defer stop()
	return u.schedule(ctx, mode, interval, ticks)
}

func (u *Updater) schedule(ctx context.Context, mode domain.Mode, interval time.Duration, ticks <-chan time.Time) error {
	n := len(u.policy.Stacks)
	c := &coordinator{root: u, busy: make([]bool, n), running: make([]bool, n), due: make([]bool, n), latest: make([][]Result, n), results: make(chan stackCompletion, n+1), interval: interval}
	c.progress, _ = u.state.Scheduler()
	c.nextTick = u.clock.Now().Add(interval)
	c.startPass(ctx, mode)
	for {
		c.dispatch()
		if c.owner != nil && c.observations == 0 && !c.applying && len(c.queue) == 0 {
			c.release()
			c.owner = nil
		}
		if c.stopping && c.owner == nil {
			return c.failure
		}
		var lost <-chan struct{}
		if c.owner != nil && !c.stopping {
			lost = c.owner.leaseCtx.Done()
		}
		select {
		case <-ctx.Done():
			c.stop(nil)
			ctx = context.WithoutCancel(ctx)
		case <-lost:
			c.stop(state.ErrLeaseLost)
		case tick, ok := <-ticks:
			if !ok {
				c.stop(nil)
				ticks = nil
				continue
			}
			if !c.stopping {
				c.nextTick = tick.Add(interval)
				for !c.nextTick.After(u.clock.Now()) {
					c.nextTick = c.nextTick.Add(interval)
				}
				c.startPass(ctx, mode)
			}
		case completion := <-c.results:
			c.complete(ctx, mode, completion)
		}
	}
}

func (c *coordinator) acquire(ctx context.Context) bool {
	if c.owner != nil {
		return true
	}
	token, acquired, err := c.root.state.AcquireLease(c.root.clock.Now(), c.root.policy.LeaseTTLSeconds)
	if err != nil {
		_ = c.root.failed(Report{RunID: newRunID(), Mode: domain.ModeMonitor}, err)
		return false
	}
	if !acquired {
		return false
	}
	c.owner, c.release = c.root.owned(ctx, token)
	c.unavailable, err = c.owner.preflight()
	if err != nil {
		_ = c.root.failed(Report{RunID: newRunID(), Mode: domain.ModeMonitor}, err)
		c.release()
		c.owner = nil
		return false
	}
	return true
}

func (c *coordinator) startPass(ctx context.Context, mode domain.Mode) {
	if !c.acquire(ctx) {
		return
	}
	pass := &observationPass{report: Report{RunID: newRunID(), Mode: domain.ModeMonitor, Actor: c.root.actor, Started: c.root.clock.Now()}}
	c.progress.NextTickAt = &c.nextTick
	if err := c.owner.state.SetSchedulerProgress(c.owner.token, c.progress, c.root.clock.Now()); err != nil {
		c.stop(err)
		return
	}
	marker, err := c.owner.state.ActiveTransaction()
	if err != nil {
		_ = c.root.failed(pass.report, err)
		return
	}
	for index, stack := range c.root.policy.Stacks {
		if !stack.Enabled {
			continue
		}
		key := state.Key{Backend: stack.Backend, Stack: stack.Name}
		if reason, down := c.unavailable[stack.Backend]; down {
			result := Result{Key: key, Code: domain.ResultEngineUnavailable, Detail: reason}
			pass.report.Results = append(pass.report.Results, result)
			c.latest[index] = []Result{result}
			continue
		}
		if marker != nil && marker.Key.Backend == stack.Backend && marker.Key.Stack == stack.Name {
			pass.report.Results = append(pass.report.Results, Result{Key: key, Code: domain.ResultBusy, Detail: "the stack belongs to an active or interrupted transaction"})
			continue
		}
		if c.busy[index] {
			if c.running[index] && (!c.applying || c.applyingIndex != index) {
				c.due[index] = true
			}
			pass.report.Results = append(pass.report.Results, Result{Key: key, Code: domain.ResultBusy, Detail: "the stack already has an observation pending"})
			continue
		}
		c.busy[index] = true
		pass.remaining++
		c.queue = append(c.queue, stackWork{index: index, pass: pass})
	}
	if pass.remaining == 0 {
		c.finishPass(pass)
		c.admit(mode)
	}
}

func (c *coordinator) dispatch() {
	if c.owner == nil {
		return
	}
	limit := c.root.policy.ObservationConcurrency
	if limit < 1 {
		limit = 1
	}
	for !c.stopping && c.observations < limit && len(c.queue) > 0 {
		work := c.queue[0]
		c.queue = c.queue[1:]
		c.observations++
		c.running[work.index] = true
		owner := c.owner
		go func() {
			results, applied, err := owner.checkStack(work.index, work.pass.report.RunID, domain.ModeMonitor, false)
			c.results <- stackCompletion{work: work, results: results, applied: applied, err: err}
		}()
	}
}

func (c *coordinator) complete(ctx context.Context, mode domain.Mode, done stackCompletion) {
	index := done.work.index
	c.busy[index] = false
	c.running[index] = false
	if done.work.apply {
		c.applying = false
		c.cooldown = c.root.clock.Now().Add(c.interval)
		report := done.work.pass.report
		report.Results = done.results
		report.UpdatesApplied = done.applied
		c.owner.finishReport(report, done.err)
		c.latest[index] = nil
	} else {
		c.observations--
		pass := done.work.pass
		pass.remaining--
		pass.report.Results = append(pass.report.Results, done.results...)
		c.latest[index] = done.results
		if done.err != nil {
			pass.failure = errors.Join(pass.failure, done.err)
			c.latest[index] = []Result{{Key: state.Key{Backend: c.root.policy.Stacks[index].Backend, Stack: c.root.policy.Stacks[index].Name}, Code: domain.ResultError, Detail: done.err.Error()}}
		}
		if pass.remaining == 0 {
			c.finishPass(pass)
		}
		if !c.stopping {
			c.admit(mode)
		}
		if c.due[index] && !c.stopping && !c.busy[index] {
			c.due[index] = false
			c.busy[index] = true
			next := &observationPass{report: Report{RunID: newRunID(), Mode: domain.ModeMonitor, Actor: c.root.actor, Started: c.root.clock.Now()}, remaining: 1}
			c.queue = append(c.queue, stackWork{index: index, pass: next})
		}
	}
	if !c.stopping {
		c.admit(mode)
	}
	_ = ctx
}

func (c *coordinator) finishPass(pass *observationPass) {
	now := c.root.clock.Now()
	c.progress.LastCompletedAt = &now
	err := c.owner.state.SetSchedulerProgress(c.owner.token, c.progress, now)
	c.owner.finishReport(pass.report, errors.Join(pass.failure, err))
}

func (c *coordinator) admit(mode domain.Mode) {
	if mode != domain.ModeApply || c.applying || c.owner == nil || c.stopping || c.root.clock.Now().Before(c.cooldown) {
		return
	}
	for index, stack := range c.root.policy.Stacks {
		if !stack.Enabled {
			continue
		}
		if c.busy[index] || c.latest[index] == nil {
			return
		}
		for _, result := range c.latest[index] {
			if result.Code != domain.ResultCandidate || !autoApply(stack, result.Key.Service) {
				continue
			}
			mature, err := c.owner.matured(result.Key, result.Digest, c.root.clock.Now())
			if err != nil || !mature {
				continue
			}
			status, err := c.owner.Status()
			if err != nil || status.BreakerOpen {
				return
			}
			marker, err := c.owner.state.ActiveTransaction()
			if err != nil || marker != nil {
				return
			}
			c.busy[index] = true
			c.applying = true
			c.applyingIndex = index
			c.due[index] = false
			pass := &observationPass{report: Report{RunID: newRunID(), Mode: domain.ModeApply, Actor: c.root.actor, Started: c.root.clock.Now()}}
			work := stackWork{index: index, pass: pass, apply: true}
			owner := c.owner
			go func() {
				results, applied, err := owner.checkStack(index, pass.report.RunID, domain.ModeApply, true)
				c.results <- stackCompletion{work: work, results: results, applied: applied, err: err}
			}()
			return
		}
	}
}

func autoApply(stack config.StackPolicy, service string) bool {
	if service == "" {
		return stack.AutoApply
	}
	for _, policy := range stack.Services {
		if policy.Name == service {
			return policy.AutoApply
		}
	}
	return false
}

func (c *coordinator) stop(err error) {
	if c.stopping {
		return
	}
	c.stopping = true
	if c.owner != nil {
		c.owner.stopReads()
	}
	c.failure = err
	for _, work := range c.queue {
		c.busy[work.index] = false
		work.pass.remaining--
		work.pass.report.Results = append(work.pass.report.Results, Result{Key: state.Key{Backend: c.root.policy.Stacks[work.index].Backend, Stack: c.root.policy.Stacks[work.index].Name}, Code: domain.ResultBusy, Detail: "observation cancelled before dispatch"})
		if work.pass.remaining == 0 {
			c.owner.finishReport(work.pass.report, context.Canceled)
		}
	}
	c.queue = nil
}

func (u *Updater) checkStack(index int, runID string, mode domain.Mode, refresh bool) ([]Result, int, error) {
	stack := u.policy.Stacks[index]
	key := state.Key{Backend: stack.Backend, Stack: stack.Name}
	if err := u.checkOwnership(); err != nil {
		return nil, 0, err
	}
	if err := u.state.StartStackCheck(u.token, key, runID, u.clock.Now()); err != nil {
		return nil, 0, err
	}
	t := &transaction{updater: u, stack: stack, runID: runID, mode: mode, refreshOnly: refresh}
	snapshot := u.gatherStack(index)
	var results []Result
	applied := 0
	if snapshot.err != nil {
		results = []Result{t.failure(key, snapshot.err)}
	} else {
		for _, observed := range snapshot.observations {
			result, changed := t.evaluate(observed, applied < 1)
			results = append(results, result)
			if changed {
				applied++
			}
		}
	}
	err := u.recordCheck(index, runID, results)
	return results, applied, err
}

func (u *Updater) finishReport(report Report, failure error) {
	if failure != nil {
		_ = u.failed(report, failure)
		return
	}
	status, err := u.Status()
	if err != nil {
		_ = u.failed(report, err)
		return
	}
	u.emit(event.RunFinished, event.Subject{RunID: report.RunID}, event.Data{Mode: string(report.Mode), UpdatesApplied: report.UpdatesApplied, BreakerOpen: status.BreakerOpen, ResultCount: len(report.Results)})
}

type stackSnapshot struct {
	observations []observation
	err          error
}

func (u *Updater) gatherStack(index int) stackSnapshot {
	t := &transaction{updater: u, stack: u.policy.Stacks[index]}
	if err := u.checkOwnership(); err != nil {
		return stackSnapshot{err: err}
	}
	snapshot, err := t.port().Observe(t.stack)
	if err != nil {
		return stackSnapshot{err: err}
	}
	observations, err := t.observe(snapshot)
	return stackSnapshot{observations: observations, err: err}
}

func (u *Updater) runFinite(report Report, unavailable map[domain.Backend]string) ([]Result, int, error) {
	marker, err := u.state.ActiveTransaction()
	if err != nil {
		return nil, 0, err
	}
	blocked := func(stack config.StackPolicy) bool {
		return marker != nil && marker.Key.Backend == stack.Backend && marker.Key.Stack == stack.Name
	}
	snapshots := make([]stackSnapshot, len(u.policy.Stacks))
	jobs := make(chan int)
	workers := u.policy.ObservationConcurrency
	if workers < 1 {
		workers = 1
	}
	var group sync.WaitGroup
	for range workers {
		group.Go(func() {
			for index := range jobs {
				snapshots[index] = u.gatherStack(index)
			}
		})
	}
	for index, stack := range u.policy.Stacks {
		if !stack.Enabled {
			continue
		}
		if blocked(stack) {
			continue
		}
		if _, down := unavailable[stack.Backend]; down {
			continue
		}
		if err := u.state.StartStackCheck(u.token, state.Key{Backend: stack.Backend, Stack: stack.Name}, report.RunID, u.clock.Now()); err != nil {
			close(jobs)
			group.Wait()
			return nil, 0, err
		}
		jobs <- index
	}
	close(jobs)
	group.Wait()
	var results []Result
	applied := 0
	for index, stack := range u.policy.Stacks {
		if !stack.Enabled {
			continue
		}
		key := state.Key{Backend: stack.Backend, Stack: stack.Name}
		if reason, down := unavailable[stack.Backend]; down {
			results = append(results, Result{Key: key, Code: domain.ResultEngineUnavailable, Detail: reason})
			continue
		}
		if blocked(stack) {
			results = append(results, Result{Key: key, Code: domain.ResultBusy, Detail: "the stack has an unfinished transaction requiring reconciliation"})
			continue
		}
		t := &transaction{updater: u, stack: stack, runID: report.RunID, mode: report.Mode}
		var outcomes []Result
		if snapshots[index].err != nil {
			outcomes = []Result{t.failure(key, snapshots[index].err)}
		} else {
			for _, observed := range snapshots[index].observations {
				result, changed := t.evaluate(observed, applied < u.policy.MaxUpdatesPerRun)
				outcomes = append(outcomes, result)
				if changed {
					applied++
				}
			}
		}
		results = append(results, outcomes...)
		if err := u.recordCheck(index, report.RunID, outcomes); err != nil {
			return results, applied, err
		}
	}
	return results, applied, nil
}

func (u *Updater) recordCheck(index int, runID string, results []Result) error {
	stack := u.policy.Stacks[index]
	outcome := "completed"
	for _, result := range results {
		if result.Code == domain.ResultError {
			outcome = "failed"
		}
		keys := []state.Key{result.Key}
		if len(stack.Services) > 0 && result.Key.Service == "" {
			keys = nil
			for _, service := range stack.Services {
				if service.Enabled {
					keys = append(keys, state.Key{Backend: stack.Backend, Stack: stack.Name, Service: service.Name})
				}
			}
		}
		for _, key := range keys {
			if err := u.state.RecordEvaluation(u.token, key, runID, result.Code, result.Detail, u.clock.Now()); err != nil {
				return err
			}
		}
	}
	return u.state.FinishStackCheck(u.token, state.Key{Backend: stack.Backend, Stack: stack.Name}, runID, outcome, u.clock.Now())
}
