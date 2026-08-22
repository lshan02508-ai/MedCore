package router

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"

	"github.com/lshan02508-ai/GOLLM/internal/registry"
)

var ErrNoAvailableWorker = errors.New("no available worker")

type workerRegistry interface {
	Available(string) []registry.WorkerSnapshot
	Snapshot(string) (registry.WorkerSnapshot, bool)
	Acquire(string) (*registry.Lease, error)
}

type Selection struct {
	Worker registry.WorkerSnapshot
	Lease  *registry.Lease
}

func (s *Selection) Release() {
	if s != nil {
		s.Lease.Release()
	}
}

// LeastInflight selects the healthy compatible worker with the fewest active
// leases. Equal-load workers are rotated to avoid a permanent ID-based bias.
type LeastInflight struct {
	registry workerRegistry
	cursor   atomic.Uint64
}

func NewLeastInflight(registry workerRegistry) *LeastInflight {
	return &LeastInflight{registry: registry}
}

func (s *LeastInflight) Select(ctx context.Context, model string, excluded map[string]struct{}) (*Selection, error) {
	candidates := s.registry.Available(model)
	candidates = slices.DeleteFunc(candidates, func(worker registry.WorkerSnapshot) bool {
		_, skip := excluded[worker.ID]
		return skip
	})
	if len(candidates) == 0 {
		return nil, fmt.Errorf("%w for model %q", ErrNoAvailableWorker, model)
	}
	slices.SortFunc(candidates, func(a, b registry.WorkerSnapshot) int {
		if a.InflightRequests != b.InflightRequests {
			if a.InflightRequests < b.InflightRequests {
				return -1
			}
			return 1
		}
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})

	lowestLoadCount := 1
	for lowestLoadCount < len(candidates) && candidates[lowestLoadCount].InflightRequests == candidates[0].InflightRequests {
		lowestLoadCount++
	}
	start := int(s.cursor.Add(1)-1) % lowestLoadCount
	ordered := make([]registry.WorkerSnapshot, 0, len(candidates))
	ordered = append(ordered, candidates[start:lowestLoadCount]...)
	ordered = append(ordered, candidates[:start]...)
	ordered = append(ordered, candidates[lowestLoadCount:]...)

	for _, candidate := range ordered {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		lease, err := s.registry.Acquire(candidate.ID)
		if err != nil {
			if errors.Is(err, registry.ErrWorkerAtCapacity) || errors.Is(err, registry.ErrWorkerUnavailable) {
				continue
			}
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			lease.Release()
			return nil, err
		}
		latest, exists := s.registry.Snapshot(candidate.ID)
		if !exists {
			lease.Release()
			continue
		}
		return &Selection{Worker: latest, Lease: lease}, nil
	}
	return nil, fmt.Errorf("%w for model %q", ErrNoAvailableWorker, model)
}
