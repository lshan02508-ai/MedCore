package scheduler

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	coremodel "github.com/lshan02508-ai/GOLLM/internal/core/model"
	"github.com/lshan02508-ai/GOLLM/internal/registry"
)

type Registry interface {
	List(string) []registry.WorkerSnapshot
	Available(string) []registry.WorkerSnapshot
	Snapshot(string) (registry.WorkerSnapshot, bool)
	Acquire(string) (*registry.Lease, error)
}

type Metrics interface {
	SchedulerEnqueued(model, class string, cost int64)
	SchedulerDispatched(model, class, worker, reason string, cost int64, queueWait time.Duration, expectedLoad, serviceRate float64)
	SchedulerRejected(model, class, reason string)
	SchedulerCompleted(model, class, worker string, cost int64, duration time.Duration, success bool)
	SchedulerState(queueDepth, queuedCost, inflightCost int64)
	SchedulerQueueState(model, class string, depth, cost int64)
	SchedulerPoolState(model string, inflightCost int64)
	SchedulerWorkerState(worker, model string, inflightCost, activeRequests int64, serviceRate float64)
}

type PoolConfig struct {
	Model           string
	MaxInflightCost int64
}

type WorkerConfig struct {
	ID                 string
	MaxInflightCost    int64
	InitialServiceRate float64
}

type Options struct {
	Registry         Registry
	Pools            []PoolConfig
	Workers          []WorkerConfig
	QueueCapacity    int
	MaxQueuedCost    int64
	MaxQueueWait     time.Duration
	RetryAfter       time.Duration
	AgingInterval    time.Duration
	MaxBypass        int
	EWMAAlpha        float64
	MinServiceRate   float64
	MaxServiceRate   float64
	Metrics          Metrics
	Now              func() time.Time
	DispatchInterval time.Duration
}

type Scheduler struct {
	options       Options
	enqueues      chan enqueue
	cancels       chan cancellation
	completions   chan completion
	done          chan struct{}
	shutdown      chan struct{}
	start         sync.Once
	stop          sync.Once
	started       atomic.Bool
	stopping      atomic.Bool
	stateSnapshot atomic.Value
}

type enqueue struct {
	ctx     context.Context
	request Request
	ack     chan enqueueResult
}

type enqueueResult struct {
	ticket *Ticket
	err    error
}

type cancellation struct {
	id  string
	err error
}

type completion struct {
	model, class, worker  string
	cost                  int64
	startedAt, finishedAt time.Time
	success               bool
}

type queuedJob struct {
	request                Request
	ctx                    context.Context
	enqueuedAt, maxQueueAt time.Time
	bypass                 int
	result                 chan DispatchResult
}

type poolState struct{ max, inflight int64 }
type workerState struct {
	max, inflight     int64
	initialRate, rate float64
	lastHealth        registry.HealthStatus
}

type runtimeState struct {
	jobs       []*queuedJob
	byID       map[string]*queuedJob
	queuedCost int64
	pools      map[string]*poolState
	workers    map[string]*workerState
	draining   bool
}

func New(options Options) (*Scheduler, error) {
	if options.Registry == nil || len(options.Pools) == 0 || len(options.Workers) == 0 {
		return nil, errors.New("scheduler registry, pools, and workers are required")
	}
	if options.QueueCapacity <= 0 || options.MaxQueuedCost <= 0 || options.MaxQueueWait <= 0 || options.RetryAfter <= 0 || options.AgingInterval <= 0 || options.MaxBypass <= 0 {
		return nil, errors.New("scheduler queue options must be positive")
	}
	if options.EWMAAlpha <= 0 || options.EWMAAlpha > 1 || options.MinServiceRate <= 0 || options.MaxServiceRate < options.MinServiceRate {
		return nil, errors.New("scheduler service rate options are invalid")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.DispatchInterval <= 0 {
		options.DispatchInterval = 25 * time.Millisecond
	}
	seenPools := map[string]struct{}{}
	for _, pool := range options.Pools {
		if pool.Model == "" || pool.MaxInflightCost <= 0 {
			return nil, errors.New("scheduler pool model and capacity are required")
		}
		if _, ok := seenPools[pool.Model]; ok {
			return nil, fmt.Errorf("scheduler pool model %q is duplicated", pool.Model)
		}
		seenPools[pool.Model] = struct{}{}
	}
	seenWorkers := map[string]struct{}{}
	for _, worker := range options.Workers {
		if worker.ID == "" || worker.MaxInflightCost <= 0 || worker.InitialServiceRate <= 0 {
			return nil, errors.New("scheduler worker ID, capacity, and initial rate are required")
		}
		if _, ok := seenWorkers[worker.ID]; ok {
			return nil, fmt.Errorf("scheduler worker %q is duplicated", worker.ID)
		}
		seenWorkers[worker.ID] = struct{}{}
	}
	scheduler := &Scheduler{options: options, enqueues: make(chan enqueue), cancels: make(chan cancellation), completions: make(chan completion), done: make(chan struct{}), shutdown: make(chan struct{})}
	scheduler.stateSnapshot.Store(Snapshot{Pools: map[string]PoolSnapshot{}, Workers: map[string]WorkerSnapshot{}})
	return scheduler, nil
}

func (s *Scheduler) Start(ctx context.Context) {
	s.start.Do(func() {
		s.started.Store(true)
		go s.run(ctx)
	})
}

func (s *Scheduler) Shutdown(ctx context.Context) error {
	s.stop.Do(func() {
		s.stopping.Store(true)
		close(s.shutdown)
	})
	if !s.started.Load() {
		return nil
	}
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Scheduler) Snapshot() Snapshot { return s.stateSnapshot.Load().(Snapshot).clone() }

func (s *Scheduler) Schedule(ctx context.Context, request Request) (*Grant, error) {
	if !s.started.Load() {
		return nil, errNotStarted
	}
	if s.stopping.Load() {
		return nil, &Error{Code: "gateway_shutting_down", Message: "the gateway scheduler is shutting down"}
	}
	ack := make(chan enqueueResult, 1)
	event := enqueue{ctx: ctx, request: request, ack: ack}
	select {
	case s.enqueues <- event:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.done:
		return nil, &Error{Code: "gateway_shutting_down", Message: "the gateway scheduler is shutting down"}
	}
	result := <-ack
	if result.err != nil {
		return nil, result.err
	}
	return result.ticket.wait(ctx)
}

func (s *Scheduler) cancel(id string, err error) {
	select {
	case s.cancels <- cancellation{id: id, err: err}:
	case <-s.done:
	}
}

func (s *Scheduler) run(ctx context.Context) {
	defer close(s.done)
	state := runtimeState{byID: make(map[string]*queuedJob), pools: make(map[string]*poolState), workers: make(map[string]*workerState)}
	for _, pool := range s.options.Pools {
		state.pools[pool.Model] = &poolState{max: pool.MaxInflightCost}
	}
	for _, worker := range s.options.Workers {
		state.workers[worker.ID] = &workerState{max: worker.MaxInflightCost, initialRate: worker.InitialServiceRate, rate: worker.InitialServiceRate}
	}
	ticker := time.NewTicker(s.options.DispatchInterval)
	defer ticker.Stop()
	ctxDone := ctx.Done()
	shutdown := s.shutdown
	for {
		if !state.draining && ctx.Err() != nil {
			s.beginShutdown(&state)
			ctxDone = nil
		}
		select {
		case event := <-s.enqueues:
			s.handleEnqueue(&state, event)
		case event := <-s.cancels:
			s.handleCancel(&state, event)
		case event := <-s.completions:
			s.handleCompletion(&state, event)
		case <-ticker.C:
		case <-ctxDone:
			s.beginShutdown(&state)
			ctxDone = nil
		case <-shutdown:
			s.beginShutdown(&state)
			shutdown = nil
		}
		now := s.options.Now()
		s.expire(&state, now)
		if !state.draining {
			s.dispatch(&state, now)
		}
		s.observeState(&state)
		if state.draining && stateInflightCost(&state) == 0 {
			return
		}
	}
}

func (s *Scheduler) handleEnqueue(state *runtimeState, event enqueue) {
	now := s.options.Now()
	r := event.request
	if state.draining || s.stopping.Load() {
		event.ack <- enqueueResult{err: &Error{Code: "gateway_shutting_down", Message: "the gateway scheduler is shutting down"}}
		return
	}
	if err := event.ctx.Err(); err != nil {
		event.ack <- enqueueResult{err: err}
		return
	}
	if r.ID == "" || r.Model == "" || r.Cost <= 0 {
		event.ack <- enqueueResult{err: errors.New("scheduler request ID, model, and cost are required")}
		return
	}
	if _, ok := coremodel.ParseWorkloadClass(r.Class); !ok {
		event.ack <- enqueueResult{err: errors.New("scheduler request workload class is invalid")}
		return
	}
	pool := s.pool(state, r.Model)
	if pool == nil {
		event.ack <- enqueueResult{err: &Error{Code: "no_available_worker", Message: "no model pool is configured for the requested model"}}
		return
	}
	if r.Cost > pool.max {
		s.reject(r, "request_cost_exceeded")
		event.ack <- enqueueResult{err: &Error{Code: "request_cost_exceeded", Message: "request cost exceeds the model pool capacity", RetryAfter: s.options.RetryAfter}}
		return
	}
	if !s.canEverFitWorker(state, r.Model, r.Cost) {
		s.reject(r, "request_cost_exceeded")
		event.ack <- enqueueResult{err: &Error{Code: "request_cost_exceeded", Message: "request cost exceeds every compatible worker capacity", RetryAfter: s.options.RetryAfter}}
		return
	}
	if _, exists := state.byID[r.ID]; exists {
		event.ack <- enqueueResult{err: errors.New("duplicate scheduler request ID")}
		return
	}
	if !s.hasHealthyWorker(r.Model) {
		s.reject(r, "no_available_worker")
		event.ack <- enqueueResult{err: &Error{Code: "no_available_worker", Message: "no healthy inference worker is available"}}
		return
	}
	if len(state.jobs) >= s.options.QueueCapacity || state.queuedCost > s.options.MaxQueuedCost-r.Cost {
		s.reject(r, "queue_full")
		event.ack <- enqueueResult{err: &Error{Code: "queue_full", Message: "the scheduler queue is full", RetryAfter: s.options.RetryAfter}}
		return
	}
	result := make(chan DispatchResult, 1)
	job := &queuedJob{request: r, ctx: event.ctx, enqueuedAt: now, maxQueueAt: now.Add(s.options.MaxQueueWait), result: result}
	state.jobs = append(state.jobs, job)
	state.byID[r.ID] = job
	state.queuedCost += r.Cost
	if s.options.Metrics != nil {
		s.options.Metrics.SchedulerEnqueued(s.metricModel(r.Model), r.Class, r.Cost)
	}
	event.ack <- enqueueResult{ticket: &Ticket{id: r.ID, scheduler: s, result: result}}
}

func (s *Scheduler) handleCancel(state *runtimeState, event cancellation) {
	job, ok := state.byID[event.id]
	if !ok {
		return
	}
	s.removeJob(state, job)
	s.reject(job.request, contextReason(event.err))
	job.result <- DispatchResult{Err: event.err}
}

func (s *Scheduler) handleCompletion(state *runtimeState, event completion) {
	pool := s.pool(state, event.model)
	if pool != nil {
		pool.inflight -= event.cost
		if pool.inflight < 0 {
			pool.inflight = 0
		}
	}
	worker := state.workers[event.worker]
	if worker != nil {
		worker.inflight -= event.cost
		if worker.inflight < 0 {
			worker.inflight = 0
		}
		duration := event.finishedAt.Sub(event.startedAt)
		if event.success && duration > 0 {
			observed := float64(event.cost) / duration.Seconds()
			observed = math.Max(s.options.MinServiceRate, math.Min(s.options.MaxServiceRate, observed))
			worker.rate = s.options.EWMAAlpha*observed + (1-s.options.EWMAAlpha)*worker.rate
		}
		if s.options.Metrics != nil {
			s.options.Metrics.SchedulerCompleted(s.metricModel(event.model), event.class, event.worker, event.cost, duration, event.success)
		}
	}
}

func (s *Scheduler) expire(state *runtimeState, now time.Time) {
	for i := len(state.jobs) - 1; i >= 0; i-- {
		job := state.jobs[i]
		code, message := "", ""
		if err := job.ctx.Err(); err != nil {
			s.removeJob(state, job)
			s.reject(job.request, contextReason(err))
			job.result <- DispatchResult{Err: err}
			continue
		}
		if !s.hasHealthyWorker(job.request.Model) {
			code, message = "no_available_worker", "no healthy inference worker is available"
		} else if !job.request.Deadline.IsZero() && !now.Before(job.request.Deadline) {
			code, message = "deadline_exceeded", "the request deadline expired before dispatch"
		} else if !now.Before(job.maxQueueAt) {
			code, message = "queue_timeout", "the request exceeded the maximum queue wait"
		}
		if code != "" {
			s.removeJob(state, job)
			s.reject(job.request, code)
			job.result <- DispatchResult{Err: &Error{Code: code, Message: message, RetryAfter: s.options.RetryAfter, QueueWait: now.Sub(job.enqueuedAt)}}
		}
	}
}

func (s *Scheduler) dispatch(state *runtimeState, now time.Time) {
	for len(state.jobs) > 0 {
		ordered := slices.Clone(state.jobs)
		slices.SortFunc(ordered, func(a, b *queuedJob) int {
			if s.better(a, b, now) {
				return -1
			}
			if s.better(b, a, now) {
				return 1
			}
			return 0
		})
		primary := ordered[0]
		dispatched := false
		for _, job := range ordered {
			samePool := s.poolKey(job.request.Model) == s.poolKey(primary.request.Model)
			realtimeOverride := job.request.Class == string(coremodel.WorkloadRealtime) && primary.request.Class != string(coremodel.WorkloadRealtime)
			if job != primary && samePool && primary.bypass >= s.options.MaxBypass && !realtimeOverride {
				continue
			}
			grant, ok := s.tryGrant(state, job, now)
			if !ok {
				continue
			}
			if job != primary && samePool {
				primary.bypass++
				if realtimeOverride && primary.bypass > s.options.MaxBypass {
					grant.Reason = "realtime_override"
				} else {
					grant.Reason = "short_job_backfill"
				}
			} else if primary.bypass >= s.options.MaxBypass {
				grant.Reason = "starvation_guard"
			} else if s.rank(job, now) > s.baseRank(job.request.Class) {
				grant.Reason = "aging_promoted"
			} else if !job.request.Deadline.IsZero() && grant.QueueWait > s.options.DispatchInterval {
				grant.Reason = "deadline_urgent"
			}
			s.removeJob(state, job)
			if s.options.Metrics != nil {
				s.options.Metrics.SchedulerDispatched(s.metricModel(job.request.Model), job.request.Class, grant.Worker.ID, grant.Reason, job.request.Cost, grant.QueueWait, grant.ExpectedLoad, grant.ServiceRate)
			}
			job.result <- DispatchResult{Grant: grant}
			dispatched = true
			break
		}
		if !dispatched {
			return
		}
	}
}

func (s *Scheduler) tryGrant(state *runtimeState, job *queuedJob, now time.Time) (*Grant, bool) {
	pool := s.pool(state, job.request.Model)
	if pool == nil || pool.inflight > pool.max-job.request.Cost {
		return nil, false
	}
	type candidate struct {
		snapshot registry.WorkerSnapshot
		load     float64
		state    *workerState
	}
	candidates := make([]candidate, 0)
	for _, snapshot := range s.options.Registry.Available(job.request.Model) {
		worker := state.workers[snapshot.ID]
		if worker == nil || worker.inflight > worker.max-job.request.Cost {
			continue
		}
		rate := math.Max(worker.rate, s.options.MinServiceRate)
		candidates = append(candidates, candidate{snapshot: snapshot, load: float64(worker.inflight+job.request.Cost) / rate, state: worker})
	}
	slices.SortFunc(candidates, func(a, b candidate) int {
		if a.load < b.load {
			return -1
		}
		if a.load > b.load {
			return 1
		}
		if a.state.inflight < b.state.inflight {
			return -1
		}
		if a.state.inflight > b.state.inflight {
			return 1
		}
		if a.snapshot.InflightRequests < b.snapshot.InflightRequests {
			return -1
		}
		if a.snapshot.InflightRequests > b.snapshot.InflightRequests {
			return 1
		}
		if a.snapshot.ID < b.snapshot.ID {
			return -1
		}
		if a.snapshot.ID > b.snapshot.ID {
			return 1
		}
		return 0
	})
	for _, candidate := range candidates {
		registryLease, err := s.options.Registry.Acquire(candidate.snapshot.ID)
		if err != nil {
			continue
		}
		pool.inflight += job.request.Cost
		candidate.state.inflight += job.request.Cost
		queueWait := now.Sub(job.enqueuedAt)
		reason := "immediate_capacity"
		if queueWait > s.options.DispatchInterval {
			reason = "priority_dispatch"
		}
		lease := &DispatchLease{scheduler: s, registryLease: registryLease, completion: completion{model: job.request.Model, class: job.request.Class, worker: candidate.snapshot.ID, cost: job.request.Cost, startedAt: now}}
		grant := &Grant{
			Worker: candidate.snapshot, Lease: lease, QueueWait: queueWait, Reason: reason,
			ExpectedLoad: candidate.load, WorkerInflightCost: candidate.state.inflight,
			ServiceRate: candidate.state.rate,
		}
		return grant, true
	}
	return nil, false
}

func (s *Scheduler) better(a, b *queuedJob, now time.Time) bool {
	ra, rb := s.rank(a, now), s.rank(b, now)
	if ra != rb {
		return ra > rb
	}
	da, db := a.request.Deadline, b.request.Deadline
	if da.IsZero() != db.IsZero() {
		return !da.IsZero()
	}
	if !da.Equal(db) {
		return da.Before(db)
	}
	if !a.enqueuedAt.Equal(b.enqueuedAt) {
		return a.enqueuedAt.Before(b.enqueuedAt)
	}
	return a.request.ID < b.request.ID
}

func (s *Scheduler) rank(job *queuedJob, now time.Time) int {
	base := s.baseRank(job.request.Class)
	promotions := int(now.Sub(job.enqueuedAt) / s.options.AgingInterval)
	if promotions < 0 {
		promotions = 0
	}
	return base + promotions
}

func (*Scheduler) baseRank(class string) int {
	return map[string]int{"batch": 0, "long_context": 1, "interactive": 2, "realtime": 3}[class]
}

func (s *Scheduler) removeJob(state *runtimeState, job *queuedJob) {
	for i, current := range state.jobs {
		if current == job {
			state.jobs = slices.Delete(state.jobs, i, i+1)
			break
		}
	}
	delete(state.byID, job.request.ID)
	state.queuedCost -= job.request.Cost
	if state.queuedCost < 0 {
		state.queuedCost = 0
	}
}

func (s *Scheduler) pool(state *runtimeState, model string) *poolState {
	if p := state.pools[model]; p != nil {
		return p
	}
	return state.pools["*"]
}

func (s *Scheduler) hasHealthyWorker(model string) bool {
	for _, worker := range s.options.Registry.List(model) {
		if worker.Health == registry.HealthHealthy {
			return true
		}
	}
	return false
}

func (s *Scheduler) canEverFitWorker(state *runtimeState, model string, cost int64) bool {
	for _, snapshot := range s.options.Registry.List(model) {
		if worker := state.workers[snapshot.ID]; worker != nil && cost <= worker.max {
			return true
		}
	}
	return false
}

func (s *Scheduler) reject(request Request, reason string) {
	if s.options.Metrics != nil {
		s.options.Metrics.SchedulerRejected(s.metricModel(request.Model), request.Class, reason)
	}
}

func (s *Scheduler) metricModel(model string) string {
	for _, pool := range s.options.Pools {
		if pool.Model == model {
			return model
		}
	}
	return "*"
}

func (s *Scheduler) poolKey(model string) string {
	return s.metricModel(model)
}

func (s *Scheduler) beginShutdown(state *runtimeState) {
	if state.draining {
		return
	}
	s.stopping.Store(true)
	state.draining = true
	s.failQueued(state, &Error{Code: "gateway_shutting_down", Message: "the gateway scheduler is shutting down"})
}

func stateInflightCost(state *runtimeState) int64 {
	var total int64
	for _, pool := range state.pools {
		total += pool.inflight
	}
	return total
}

func (s *Scheduler) failQueued(state *runtimeState, err error) {
	for _, job := range state.jobs {
		var schedulerError *Error
		if errors.As(err, &schedulerError) {
			s.reject(job.request, schedulerError.Code)
		}
		job.result <- DispatchResult{Err: err}
	}
	state.jobs = nil
	state.byID = map[string]*queuedJob{}
	state.queuedCost = 0
}

func contextReason(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline_exceeded"
	}
	return "request_canceled"
}

func (s *Scheduler) observeState(state *runtimeState) {
	snapshot := Snapshot{QueueDepth: int64(len(state.jobs)), QueuedCost: state.queuedCost, Pools: make(map[string]PoolSnapshot, len(state.pools)), Workers: make(map[string]WorkerSnapshot, len(state.workers))}
	for model, pool := range state.pools {
		snapshot.InflightCost += pool.inflight
		snapshot.Pools[model] = PoolSnapshot{InflightCost: pool.inflight, MaxInflightCost: pool.max}
	}
	for id, worker := range state.workers {
		registered, ok := s.options.Registry.Snapshot(id)
		if !ok {
			continue
		}
		if worker.lastHealth != "" && worker.lastHealth != registry.HealthHealthy && registered.Health == registry.HealthHealthy {
			worker.rate = worker.initialRate
		}
		worker.lastHealth = registered.Health
		snapshot.Workers[id] = WorkerSnapshot{InflightCost: worker.inflight, MaxInflightCost: worker.max, ActiveRequests: registered.InflightRequests, ServiceRate: worker.rate, Health: registered.Health}
	}
	s.stateSnapshot.Store(snapshot)
	if s.options.Metrics == nil {
		return
	}
	for model, pool := range snapshot.Pools {
		s.options.Metrics.SchedulerPoolState(model, pool.InflightCost)
	}
	s.options.Metrics.SchedulerState(snapshot.QueueDepth, snapshot.QueuedCost, snapshot.InflightCost)
	type queueKey struct{ model, class string }
	queueDepth := map[queueKey]int64{}
	queueCost := map[queueKey]int64{}
	for _, job := range state.jobs {
		key := queueKey{s.metricModel(job.request.Model), job.request.Class}
		queueDepth[key]++
		queueCost[key] += job.request.Cost
	}
	for key, depth := range queueDepth {
		s.options.Metrics.SchedulerQueueState(key.model, key.class, depth, queueCost[key])
	}
	for id, worker := range snapshot.Workers {
		registered, _ := s.options.Registry.Snapshot(id)
		model := "*"
		if len(registered.Models) > 0 {
			model = registered.Models[0]
		}
		s.options.Metrics.SchedulerWorkerState(id, model, worker.InflightCost, worker.ActiveRequests, worker.ServiceRate)
	}
}
