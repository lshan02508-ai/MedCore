package registry

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

type HealthProbe interface {
	Ready(context.Context) error
}

type HealthTarget struct {
	WorkerID string
	Probe    HealthProbe
}

type HealthMonitorMetrics interface {
	ObserveWorkerHealthCheck(failed bool)
	SetWorkerCounts(total, healthy int64)
}

type healthMonitorRegistry interface {
	Snapshot(string) (WorkerSnapshot, bool)
	List(string) []WorkerSnapshot
	Available(string) []WorkerSnapshot
	RecordHealthSuccess(string, time.Time, bool) error
	RecordHealthFailure(string, string, time.Time, bool) error
}

type HealthMonitorOptions struct {
	Registry           healthMonitorRegistry
	Targets            []HealthTarget
	Interval           time.Duration
	Timeout            time.Duration
	UnhealthyThreshold int
	HealthyThreshold   int
	Metrics            HealthMonitorMetrics
}

type HealthMonitor struct {
	registry           healthMonitorRegistry
	targets            []HealthTarget
	interval           time.Duration
	timeout            time.Duration
	unhealthyThreshold int
	healthyThreshold   int
	metrics            HealthMonitorMetrics

	checkMu sync.Mutex
	streaks map[string]healthStreak
	start   sync.Once
}

type healthStreak struct {
	successes int
	failures  int
}

type probeResult struct {
	workerID string
	err      error
}

func NewHealthMonitor(opts HealthMonitorOptions) (*HealthMonitor, error) {
	if opts.Registry == nil {
		return nil, errors.New("health monitor registry is required")
	}
	if len(opts.Targets) == 0 {
		return nil, errors.New("health monitor requires at least one target")
	}
	if opts.Interval <= 0 || opts.Timeout <= 0 {
		return nil, errors.New("health monitor interval and timeout must be positive")
	}
	if opts.UnhealthyThreshold <= 0 || opts.HealthyThreshold <= 0 {
		return nil, errors.New("health monitor thresholds must be positive")
	}
	seen := make(map[string]struct{}, len(opts.Targets))
	for _, target := range opts.Targets {
		if target.WorkerID == "" || target.Probe == nil {
			return nil, errors.New("health target requires worker ID and probe")
		}
		if _, exists := seen[target.WorkerID]; exists {
			return nil, fmt.Errorf("health target %q is duplicated", target.WorkerID)
		}
		if _, exists := opts.Registry.Snapshot(target.WorkerID); !exists {
			return nil, fmt.Errorf("%w: %s", ErrWorkerNotFound, target.WorkerID)
		}
		seen[target.WorkerID] = struct{}{}
	}
	return &HealthMonitor{
		registry: opts.Registry, targets: opts.Targets, interval: opts.Interval, timeout: opts.Timeout,
		unhealthyThreshold: opts.UnhealthyThreshold, healthyThreshold: opts.HealthyThreshold,
		metrics: opts.Metrics, streaks: make(map[string]healthStreak, len(opts.Targets)),
	}, nil
}

func (m *HealthMonitor) Start(ctx context.Context) {
	m.start.Do(func() {
		go m.run(ctx)
	})
}

func (m *HealthMonitor) run(ctx context.Context) {
	m.CheckOnce(ctx)
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.CheckOnce(ctx)
		}
	}
}

// CheckOnce probes every Worker concurrently. Calls are serialized so health
// streaks remain deterministic even if a manual check overlaps a ticker.
func (m *HealthMonitor) CheckOnce(ctx context.Context) {
	m.checkMu.Lock()
	defer m.checkMu.Unlock()

	results := make(chan probeResult, len(m.targets))
	for _, target := range m.targets {
		go func() {
			probeCtx, cancel := context.WithTimeout(ctx, m.timeout)
			defer cancel()
			results <- probeResult{workerID: target.WorkerID, err: target.Probe.Ready(probeCtx)}
		}()
	}
	for range m.targets {
		select {
		case <-ctx.Done():
			m.updateWorkerMetrics()
			return
		case result := <-results:
			m.applyResult(result, time.Now())
		}
	}
	m.updateWorkerMetrics()
}

func (m *HealthMonitor) applyResult(result probeResult, checkedAt time.Time) {
	streak := m.streaks[result.workerID]
	snapshot, exists := m.registry.Snapshot(result.workerID)
	if !exists {
		return
	}
	if result.err == nil {
		streak.failures = 0
		streak.successes++
		m.observe(false)
		markHealthy := snapshot.ObservedHealth == HealthUnknown || snapshot.ObservedHealth == HealthHealthy || streak.successes >= m.healthyThreshold
		_ = m.registry.RecordHealthSuccess(result.workerID, checkedAt, markHealthy)
	} else {
		streak.successes = 0
		streak.failures++
		m.observe(true)
		_ = m.registry.RecordHealthFailure(result.workerID, result.err.Error(), checkedAt, streak.failures >= m.unhealthyThreshold)
	}
	m.streaks[result.workerID] = streak
}

func (m *HealthMonitor) observe(failed bool) {
	if m.metrics != nil {
		m.metrics.ObserveWorkerHealthCheck(failed)
	}
}

func (m *HealthMonitor) updateWorkerMetrics() {
	if m.metrics != nil {
		m.metrics.SetWorkerCounts(int64(len(m.registry.List(""))), int64(len(m.registry.Available(""))))
	}
}
