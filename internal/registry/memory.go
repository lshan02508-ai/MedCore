package registry

import (
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

type workerState struct {
	worker              Worker
	health              HealthStatus
	draining            bool
	consecutiveFailures uint64
	lastCheckedAt       time.Time
	lastFailure         string
	inflight            atomic.Int64
}

// Memory is a process-local, concurrency-safe Worker Registry. It is the
// default implementation for a single Gateway instance.
type Memory struct {
	mu      sync.RWMutex
	workers map[string]*workerState
}

var _ Registry = (*Memory)(nil)

func NewMemory(workers []Worker) (*Memory, error) {
	registry := &Memory{workers: make(map[string]*workerState, len(workers))}
	for _, worker := range workers {
		if err := registry.Register(worker); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

func (r *Memory) Register(worker Worker) error {
	normalized, err := normalizeWorker(worker)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.workers == nil {
		r.workers = make(map[string]*workerState)
	}
	if _, exists := r.workers[normalized.ID]; exists {
		return fmt.Errorf("worker %q is already registered", normalized.ID)
	}
	r.workers[normalized.ID] = &workerState{worker: normalized, health: HealthUnknown}
	return nil
}

func (r *Memory) Remove(workerID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	state, exists := r.workers[workerID]
	if !exists {
		return fmt.Errorf("%w: %s", ErrWorkerNotFound, workerID)
	}
	if state.inflight.Load() != 0 {
		return fmt.Errorf("%w: %s", ErrWorkerBusy, workerID)
	}
	delete(r.workers, workerID)
	return nil
}

func (r *Memory) Snapshot(workerID string) (WorkerSnapshot, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	state, exists := r.workers[workerID]
	if !exists {
		return WorkerSnapshot{}, false
	}
	return snapshotOf(state), true
}

// List returns deterministic snapshots. An empty model includes every worker.
func (r *Memory) List(model string) []WorkerSnapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	snapshots := make([]WorkerSnapshot, 0, len(r.workers))
	for _, state := range r.workers {
		if model == "" || state.worker.Supports(model) {
			snapshots = append(snapshots, snapshotOf(state))
		}
	}
	slices.SortFunc(snapshots, func(a, b WorkerSnapshot) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	return snapshots
}

func (r *Memory) Available(model string) []WorkerSnapshot {
	snapshots := r.List(model)
	return slices.DeleteFunc(snapshots, func(snapshot WorkerSnapshot) bool {
		return !snapshot.Available()
	})
}

func (r *Memory) MarkHealthy(workerID string, checkedAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	state, exists := r.workers[workerID]
	if !exists {
		return fmt.Errorf("%w: %s", ErrWorkerNotFound, workerID)
	}
	state.health = HealthHealthy
	state.consecutiveFailures = 0
	state.lastCheckedAt = checkedAt
	state.lastFailure = ""
	return nil
}

func (r *Memory) MarkUnhealthy(workerID, reason string, checkedAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	state, exists := r.workers[workerID]
	if !exists {
		return fmt.Errorf("%w: %s", ErrWorkerNotFound, workerID)
	}
	state.health = HealthUnhealthy
	state.consecutiveFailures++
	state.lastCheckedAt = checkedAt
	state.lastFailure = reason
	return nil
}

func (r *Memory) SetDraining(workerID string, draining bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	state, exists := r.workers[workerID]
	if !exists {
		return fmt.Errorf("%w: %s", ErrWorkerNotFound, workerID)
	}
	state.draining = draining
	return nil
}

func (r *Memory) Acquire(workerID string) (*Lease, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	state, exists := r.workers[workerID]
	if !exists {
		return nil, fmt.Errorf("%w: %s", ErrWorkerNotFound, workerID)
	}
	if state.draining || state.health != HealthHealthy {
		status := state.health
		if state.draining {
			status = HealthDraining
		}
		return nil, fmt.Errorf("%w: %s is %s", ErrWorkerUnavailable, workerID, status)
	}
	for {
		inflight := state.inflight.Load()
		if state.worker.MaxConcurrency > 0 && inflight >= state.worker.MaxConcurrency {
			return nil, fmt.Errorf("%w: %s", ErrWorkerAtCapacity, workerID)
		}
		if state.inflight.CompareAndSwap(inflight, inflight+1) {
			return &Lease{state: state}, nil
		}
	}
}

func snapshotOf(state *workerState) WorkerSnapshot {
	worker := state.worker
	worker.Models = slices.Clone(worker.Models)
	health := state.health
	if state.draining {
		health = HealthDraining
	}
	return WorkerSnapshot{
		Worker:              worker,
		Health:              health,
		InflightRequests:    state.inflight.Load(),
		ConsecutiveFailures: state.consecutiveFailures,
		LastCheckedAt:       state.lastCheckedAt,
		LastFailure:         state.lastFailure,
	}
}

// Lease accounts for one in-flight request. Release is safe to call multiple
// times; capacity is returned exactly once.
type Lease struct {
	state *workerState
	once  sync.Once
}

func (l *Lease) Release() {
	if l == nil || l.state == nil {
		return
	}
	l.once.Do(func() {
		l.state.inflight.Add(-1)
	})
}
