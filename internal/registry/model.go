package registry

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"
)

var (
	ErrWorkerNotFound    = errors.New("worker not found")
	ErrWorkerUnavailable = errors.New("worker unavailable")
	ErrWorkerAtCapacity  = errors.New("worker at capacity")
	ErrWorkerBusy        = errors.New("worker has in-flight requests")
)

type HealthStatus string

const (
	HealthUnknown   HealthStatus = "unknown"
	HealthHealthy   HealthStatus = "healthy"
	HealthUnhealthy HealthStatus = "unhealthy"
	HealthDraining  HealthStatus = "draining"
)

// Worker is immutable registration data. MaxConcurrency equal to zero means
// that the registry does not impose a static concurrency limit.
type Worker struct {
	ID                 string
	Endpoint           string
	Models             []string
	MaxConcurrency     int64
	MaxInflightCost    int64
	InitialServiceRate float64
}

func (w Worker) Supports(model string) bool {
	return slices.Contains(w.Models, "*") || slices.Contains(w.Models, model)
}

// WorkerSnapshot is a point-in-time copy safe for schedulers and routers to
// inspect without accessing mutable registry state.
type WorkerSnapshot struct {
	Worker
	Health              HealthStatus
	ObservedHealth      HealthStatus
	InflightRequests    int64
	ConsecutiveFailures uint64
	LastCheckedAt       time.Time
	LastFailure         string
}

// Registry is the state boundary consumed by future health checking, routing,
// and dispatch components.
type Registry interface {
	Register(Worker) error
	Remove(string) error
	Snapshot(string) (WorkerSnapshot, bool)
	List(string) []WorkerSnapshot
	Available(string) []WorkerSnapshot
	MarkHealthy(string, time.Time) error
	MarkUnhealthy(string, string, time.Time) error
	RecordHealthSuccess(string, time.Time, bool) error
	RecordHealthFailure(string, string, time.Time, bool) error
	SetDraining(string, bool) error
	Acquire(string) (*Lease, error)
}

func (s WorkerSnapshot) Available() bool {
	return s.Health == HealthHealthy && (s.MaxConcurrency == 0 || s.InflightRequests < s.MaxConcurrency)
}

func normalizeWorker(worker Worker) (Worker, error) {
	worker.ID = strings.TrimSpace(worker.ID)
	if worker.ID == "" {
		return Worker{}, errors.New("worker ID is required")
	}
	if worker.MaxConcurrency < 0 {
		return Worker{}, fmt.Errorf("worker %q max concurrency must not be negative", worker.ID)
	}
	if worker.MaxInflightCost < 0 || worker.InitialServiceRate < 0 {
		return Worker{}, fmt.Errorf("worker %q cost capacity and service rate must not be negative", worker.ID)
	}

	endpoint, err := url.Parse(strings.TrimSpace(worker.Endpoint))
	if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Host == "" {
		return Worker{}, fmt.Errorf("worker %q has invalid endpoint %q", worker.ID, worker.Endpoint)
	}
	if endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return Worker{}, fmt.Errorf("worker %q endpoint must not contain credentials, query, or fragment", worker.ID)
	}
	worker.Endpoint = strings.TrimRight(endpoint.String(), "/")

	seenModels := make(map[string]struct{}, len(worker.Models))
	models := make([]string, 0, len(worker.Models))
	for _, model := range worker.Models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if _, exists := seenModels[model]; exists {
			continue
		}
		seenModels[model] = struct{}{}
		models = append(models, model)
	}
	if len(models) == 0 {
		return Worker{}, fmt.Errorf("worker %q must support at least one model", worker.ID)
	}
	slices.Sort(models)
	worker.Models = models
	return worker, nil
}
