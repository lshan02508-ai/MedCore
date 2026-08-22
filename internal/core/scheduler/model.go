package scheduler

import (
	"context"
	"errors"
	"maps"
	"sync"
	"time"

	"github.com/lshan02508-ai/GOLLM/internal/registry"
)

type Error struct {
	Code       string
	Message    string
	RetryAfter time.Duration
	QueueWait  time.Duration
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

type Request struct {
	ID       string
	Model    string
	Class    string
	Cost     int64
	Deadline time.Time
}

type Grant struct {
	Worker             registry.WorkerSnapshot
	Lease              *DispatchLease
	QueueWait          time.Duration
	Reason             string
	ExpectedLoad       float64
	WorkerInflightCost int64
	ServiceRate        float64
}

type DispatchLease struct {
	scheduler     *Scheduler
	registryLease *registry.Lease
	completion    completion
	once          sync.Once
}

func (l *DispatchLease) Complete(success bool) {
	if l == nil {
		return
	}
	l.once.Do(func() {
		if l.registryLease != nil {
			l.registryLease.Release()
		}
		l.completion.success = success
		l.completion.finishedAt = l.scheduler.options.Now()
		select {
		case l.scheduler.completions <- l.completion:
		case <-l.scheduler.done:
		}
	})
}

type Ticket struct {
	id        string
	scheduler *Scheduler
	result    <-chan DispatchResult
}

type DispatchResult struct {
	Grant *Grant
	Err   error
}

func (t *Ticket) wait(ctx context.Context) (*Grant, error) {
	select {
	case result := <-t.result:
		return result.Grant, result.Err
	case <-ctx.Done():
		t.scheduler.cancel(t.id, ctx.Err())
		result := <-t.result
		if result.Grant != nil && result.Grant.Lease != nil {
			result.Grant.Lease.Complete(false)
		}
		return nil, ctx.Err()
	}
}

var errNotStarted = errors.New("scheduler is not started")

type PoolSnapshot struct{ InflightCost, MaxInflightCost int64 }
type WorkerSnapshot struct {
	InflightCost, MaxInflightCost, ActiveRequests int64
	ServiceRate                                   float64
	Health                                        registry.HealthStatus
}
type Snapshot struct {
	QueueDepth, QueuedCost, InflightCost int64
	Pools                                map[string]PoolSnapshot
	Workers                              map[string]WorkerSnapshot
}

func (s Snapshot) clone() Snapshot {
	s.Pools = maps.Clone(s.Pools)
	s.Workers = maps.Clone(s.Workers)
	return s
}
