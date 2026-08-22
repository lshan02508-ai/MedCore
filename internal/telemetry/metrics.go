package telemetry

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Metrics struct {
	requests              atomic.Uint64
	inflight              atomic.Int64
	errors                atomic.Uint64
	firstTokenTimeouts    atomic.Uint64
	streamIdleTimeouts    atomic.Uint64
	workerHealthChecks    atomic.Uint64
	workerHealthFailures  atomic.Uint64
	workersTotal          atomic.Int64
	workersHealthy        atomic.Int64
	schedulerQueueDepth   atomic.Int64
	schedulerQueuedCost   atomic.Int64
	schedulerInflightCost atomic.Int64
	schedulerMu           sync.RWMutex
	schedulerEnqueued     map[schedulerKey]uint64
	schedulerDispatched   map[schedulerReasonKey]uint64
	schedulerRejected     map[schedulerReasonKey]uint64
	schedulerCompleted    map[schedulerWorkerKey]uint64
	schedulerFailed       map[schedulerWorkerKey]uint64
	queueWaitSum          map[schedulerKey]float64
	queueWaitCount        map[schedulerKey]uint64
	requestCostSum        map[schedulerKey]uint64
	requestCostCount      map[schedulerKey]uint64
	requestDurationSum    map[schedulerWorkerKey]float64
	ttftSum               map[schedulerWorkerKey]float64
	ttftCount             map[schedulerWorkerKey]uint64
	serviceRates          map[workerKey]float64
	queueDepthByKey       map[schedulerKey]int64
	queuedCostByKey       map[schedulerKey]int64
	poolInflightCost      map[string]int64
	workerInflightCost    map[workerKey]int64
	workerActive          map[workerKey]int64
}

type schedulerKey struct{ model, class string }
type schedulerReasonKey struct{ model, class, reason string }
type schedulerWorkerKey struct{ model, class, worker string }
type workerKey struct{ model, worker string }

func (m *Metrics) Begin() func(bool) {
	m.requests.Add(1)
	m.inflight.Add(1)
	return func(failed bool) {
		m.inflight.Add(-1)
		if failed {
			m.errors.Add(1)
		}
	}
}

func (m *Metrics) FirstTokenTimeout() {
	m.firstTokenTimeouts.Add(1)
}

func (m *Metrics) StreamIdleTimeout() {
	m.streamIdleTimeouts.Add(1)
}

func (m *Metrics) ObserveWorkerHealthCheck(failed bool) {
	m.workerHealthChecks.Add(1)
	if failed {
		m.workerHealthFailures.Add(1)
	}
}

func (m *Metrics) SetWorkerCounts(total, healthy int64) {
	m.workersTotal.Store(total)
	m.workersHealthy.Store(healthy)
}

func (m *Metrics) SchedulerEnqueued(model, class string, cost int64) {
	m.schedulerMu.Lock()
	defer m.schedulerMu.Unlock()
	m.initSchedulerMaps()
	key := schedulerKey{model, class}
	m.schedulerEnqueued[key]++
	m.requestCostSum[key] += uint64(cost)
	m.requestCostCount[key]++
}

func (m *Metrics) SchedulerDispatched(model, class, worker, reason string, _ int64, queueWait time.Duration, _ float64, serviceRate float64) {
	m.schedulerMu.Lock()
	defer m.schedulerMu.Unlock()
	m.initSchedulerMaps()
	m.schedulerDispatched[schedulerReasonKey{model, class, reason}]++
	key := schedulerKey{model, class}
	m.queueWaitSum[key] += queueWait.Seconds()
	m.queueWaitCount[key]++
	m.serviceRates[workerKey{model, worker}] = serviceRate
}

func (m *Metrics) SchedulerRejected(model, class, reason string) {
	m.schedulerMu.Lock()
	defer m.schedulerMu.Unlock()
	m.initSchedulerMaps()
	m.schedulerRejected[schedulerReasonKey{model, class, reason}]++
}

func (m *Metrics) SchedulerCompleted(model, class, worker string, _ int64, duration time.Duration, success bool) {
	m.schedulerMu.Lock()
	defer m.schedulerMu.Unlock()
	m.initSchedulerMaps()
	key := schedulerWorkerKey{model, class, worker}
	m.schedulerCompleted[key]++
	if !success {
		m.schedulerFailed[key]++
	}
	m.requestDurationSum[key] += duration.Seconds()
}

func (m *Metrics) ObserveRequestTTFT(model, class, worker string, duration time.Duration) {
	m.schedulerMu.Lock()
	defer m.schedulerMu.Unlock()
	m.initSchedulerMaps()
	key := schedulerWorkerKey{model, class, worker}
	m.ttftSum[key] += duration.Seconds()
	m.ttftCount[key]++
}

func (m *Metrics) SchedulerState(queueDepth, queuedCost, inflightCost int64) {
	m.schedulerQueueDepth.Store(queueDepth)
	m.schedulerQueuedCost.Store(queuedCost)
	m.schedulerInflightCost.Store(inflightCost)
	m.schedulerMu.Lock()
	defer m.schedulerMu.Unlock()
	m.initSchedulerMaps()
	for key := range m.queueDepthByKey {
		m.queueDepthByKey[key] = 0
		m.queuedCostByKey[key] = 0
	}
}

func (m *Metrics) SchedulerQueueState(model, class string, depth, cost int64) {
	m.schedulerMu.Lock()
	defer m.schedulerMu.Unlock()
	m.initSchedulerMaps()
	key := schedulerKey{model, class}
	m.queueDepthByKey[key] = depth
	m.queuedCostByKey[key] = cost
}
func (m *Metrics) SchedulerPoolState(model string, inflightCost int64) {
	m.schedulerMu.Lock()
	defer m.schedulerMu.Unlock()
	m.initSchedulerMaps()
	m.poolInflightCost[model] = inflightCost
}
func (m *Metrics) SchedulerWorkerState(worker, model string, inflightCost, activeRequests int64, serviceRate float64) {
	m.schedulerMu.Lock()
	defer m.schedulerMu.Unlock()
	m.initSchedulerMaps()
	key := workerKey{model, worker}
	m.workerInflightCost[key] = inflightCost
	m.workerActive[key] = activeRequests
	m.serviceRates[key] = serviceRate
}

func (m *Metrics) initSchedulerMaps() {
	if m.schedulerEnqueued != nil {
		return
	}
	m.schedulerEnqueued = make(map[schedulerKey]uint64)
	m.schedulerDispatched = make(map[schedulerReasonKey]uint64)
	m.schedulerRejected = make(map[schedulerReasonKey]uint64)
	m.schedulerCompleted = make(map[schedulerWorkerKey]uint64)
	m.schedulerFailed = make(map[schedulerWorkerKey]uint64)
	m.queueWaitSum = make(map[schedulerKey]float64)
	m.queueWaitCount = make(map[schedulerKey]uint64)
	m.requestCostSum = make(map[schedulerKey]uint64)
	m.requestCostCount = make(map[schedulerKey]uint64)
	m.requestDurationSum = make(map[schedulerWorkerKey]float64)
	m.ttftSum = make(map[schedulerWorkerKey]float64)
	m.ttftCount = make(map[schedulerWorkerKey]uint64)
	m.serviceRates = make(map[workerKey]float64)
	m.queueDepthByKey = make(map[schedulerKey]int64)
	m.queuedCostByKey = make(map[schedulerKey]int64)
	m.poolInflightCost = make(map[string]int64)
	m.workerInflightCost = make(map[workerKey]int64)
	m.workerActive = make(map[workerKey]int64)
}

func (m *Metrics) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	fmt.Fprintf(w, "# TYPE gollm_requests_total counter\ngollm_requests_total %d\n", m.requests.Load())
	fmt.Fprintf(w, "# TYPE gollm_requests_inflight gauge\ngollm_requests_inflight %d\n", m.inflight.Load())
	fmt.Fprintf(w, "# TYPE gollm_proxy_errors_total counter\ngollm_proxy_errors_total %d\n", m.errors.Load())
	fmt.Fprintf(w, "# TYPE gollm_stream_first_token_timeouts_total counter\ngollm_stream_first_token_timeouts_total %d\n", m.firstTokenTimeouts.Load())
	fmt.Fprintf(w, "# TYPE gollm_stream_idle_timeouts_total counter\ngollm_stream_idle_timeouts_total %d\n", m.streamIdleTimeouts.Load())
	fmt.Fprintf(w, "# TYPE gollm_worker_health_checks_total counter\ngollm_worker_health_checks_total %d\n", m.workerHealthChecks.Load())
	fmt.Fprintf(w, "# TYPE gollm_worker_health_check_failures_total counter\ngollm_worker_health_check_failures_total %d\n", m.workerHealthFailures.Load())
	fmt.Fprintf(w, "# TYPE gollm_workers_total gauge\ngollm_workers_total %d\n", m.workersTotal.Load())
	fmt.Fprintf(w, "# TYPE gollm_workers_healthy gauge\ngollm_workers_healthy %d\n", m.workersHealthy.Load())
	fmt.Fprintf(w, "# TYPE gollm_scheduler_queue_depth_all gauge\ngollm_scheduler_queue_depth_all %d\n", m.schedulerQueueDepth.Load())
	fmt.Fprintf(w, "# TYPE gollm_scheduler_queued_cost_all gauge\ngollm_scheduler_queued_cost_all %d\n", m.schedulerQueuedCost.Load())
	fmt.Fprintf(w, "# TYPE gollm_model_inflight_cost_all gauge\ngollm_model_inflight_cost_all %d\n", m.schedulerInflightCost.Load())
	fmt.Fprint(w, schedulerMetricTypes)
	m.writeSchedulerMetrics(w)
}

const schedulerMetricTypes = `# TYPE gollm_scheduler_enqueued_total counter
# TYPE gollm_scheduler_dispatched_total counter
# TYPE gollm_scheduler_rejected_total counter
# TYPE gollm_scheduler_completed_total counter
# TYPE gollm_scheduler_completion_failures_total counter
# TYPE gollm_scheduler_deadline_violations_total counter
# TYPE gollm_scheduler_queue_depth gauge
# TYPE gollm_scheduler_queued_cost gauge
# TYPE gollm_scheduler_queue_wait_seconds summary
# TYPE gollm_model_inflight_cost gauge
# TYPE gollm_worker_inflight_cost gauge
# TYPE gollm_worker_active_requests gauge
# TYPE gollm_worker_service_rate gauge
# TYPE gollm_request_cost summary
# TYPE gollm_request_duration_seconds summary
# TYPE gollm_request_ttft_seconds summary
`

func (m *Metrics) writeSchedulerMetrics(w http.ResponseWriter) {
	m.schedulerMu.RLock()
	defer m.schedulerMu.RUnlock()
	keys := make([]schedulerKey, 0, len(m.schedulerEnqueued))
	for key := range m.schedulerEnqueued {
		keys = append(keys, key)
	}
	slices.SortFunc(keys, func(a, b schedulerKey) int {
		if a.model != b.model {
			return strings.Compare(a.model, b.model)
		}
		return strings.Compare(a.class, b.class)
	})
	for _, key := range keys {
		labels := fmt.Sprintf("model=\"%s\",class=\"%s\"", label(key.model), label(key.class))
		fmt.Fprintf(w, "gollm_scheduler_enqueued_total{%s} %d\n", labels, m.schedulerEnqueued[key])
		fmt.Fprintf(w, "gollm_scheduler_queue_wait_seconds_sum{%s} %g\n", labels, m.queueWaitSum[key])
		fmt.Fprintf(w, "gollm_scheduler_queue_wait_seconds_count{%s} %d\n", labels, m.queueWaitCount[key])
		fmt.Fprintf(w, "gollm_request_cost_sum{%s} %d\n", labels, m.requestCostSum[key])
		fmt.Fprintf(w, "gollm_request_cost_count{%s} %d\n", labels, m.requestCostCount[key])
	}
	reasons := make([]schedulerReasonKey, 0, len(m.schedulerDispatched)+len(m.schedulerRejected))
	seen := map[schedulerReasonKey]struct{}{}
	for key := range m.schedulerDispatched {
		seen[key] = struct{}{}
	}
	for key := range m.schedulerRejected {
		seen[key] = struct{}{}
	}
	for key := range seen {
		reasons = append(reasons, key)
	}
	slices.SortFunc(reasons, func(a, b schedulerReasonKey) int {
		if a.model != b.model {
			return strings.Compare(a.model, b.model)
		}
		if a.class != b.class {
			return strings.Compare(a.class, b.class)
		}
		return strings.Compare(a.reason, b.reason)
	})
	for _, key := range reasons {
		labels := fmt.Sprintf("model=\"%s\",class=\"%s\",reason=\"%s\"", label(key.model), label(key.class), label(key.reason))
		if value := m.schedulerDispatched[key]; value > 0 {
			fmt.Fprintf(w, "gollm_scheduler_dispatched_total{%s} %d\n", labels, value)
		}
		if value := m.schedulerRejected[key]; value > 0 {
			fmt.Fprintf(w, "gollm_scheduler_rejected_total{%s} %d\n", labels, value)
			if key.reason == "deadline_exceeded" {
				deadlineLabels := fmt.Sprintf("model=\"%s\",class=\"%s\"", label(key.model), label(key.class))
				fmt.Fprintf(w, "gollm_scheduler_deadline_violations_total{%s} %d\n", deadlineLabels, value)
			}
		}
	}
	queueKeys := make([]schedulerKey, 0, len(m.queueDepthByKey))
	for key := range m.queueDepthByKey {
		queueKeys = append(queueKeys, key)
	}
	slices.SortFunc(queueKeys, func(a, b schedulerKey) int {
		if a.model != b.model {
			return strings.Compare(a.model, b.model)
		}
		return strings.Compare(a.class, b.class)
	})
	queuedCostByModel := make(map[string]int64)
	for _, key := range queueKeys {
		labels := fmt.Sprintf("model=\"%s\",class=\"%s\"", label(key.model), label(key.class))
		fmt.Fprintf(w, "gollm_scheduler_queue_depth{%s} %d\n", labels, m.queueDepthByKey[key])
		queuedCostByModel[key.model] += m.queuedCostByKey[key]
	}
	queuedModels := make([]string, 0, len(queuedCostByModel))
	for model := range queuedCostByModel {
		queuedModels = append(queuedModels, model)
	}
	slices.Sort(queuedModels)
	for _, model := range queuedModels {
		fmt.Fprintf(w, "gollm_scheduler_queued_cost{model=\"%s\"} %d\n", label(model), queuedCostByModel[model])
	}
	workers := make([]workerKey, 0, len(m.serviceRates))
	for key := range m.serviceRates {
		workers = append(workers, key)
	}
	slices.SortFunc(workers, func(a, b workerKey) int {
		if a.model != b.model {
			return strings.Compare(a.model, b.model)
		}
		return strings.Compare(a.worker, b.worker)
	})
	for _, key := range workers {
		labels := fmt.Sprintf("worker=\"%s\",model=\"%s\"", label(key.worker), label(key.model))
		fmt.Fprintf(w, "gollm_worker_service_rate{%s} %g\n", labels, m.serviceRates[key])
		fmt.Fprintf(w, "gollm_worker_inflight_cost{%s} %d\n", labels, m.workerInflightCost[key])
		fmt.Fprintf(w, "gollm_worker_active_requests{%s} %d\n", labels, m.workerActive[key])
	}
	poolModels := make([]string, 0, len(m.poolInflightCost))
	for model := range m.poolInflightCost {
		poolModels = append(poolModels, model)
	}
	slices.Sort(poolModels)
	for _, model := range poolModels {
		fmt.Fprintf(w, "gollm_model_inflight_cost{model=\"%s\"} %d\n", label(model), m.poolInflightCost[model])
	}
	completed := make([]schedulerWorkerKey, 0, len(m.schedulerCompleted)+len(m.ttftCount))
	completedSet := make(map[schedulerWorkerKey]struct{}, len(m.schedulerCompleted)+len(m.ttftCount))
	for key := range m.schedulerCompleted {
		completedSet[key] = struct{}{}
	}
	for key := range m.ttftCount {
		completedSet[key] = struct{}{}
	}
	for key := range completedSet {
		completed = append(completed, key)
	}
	slices.SortFunc(completed, func(a, b schedulerWorkerKey) int {
		if a.model != b.model {
			return strings.Compare(a.model, b.model)
		}
		if a.class != b.class {
			return strings.Compare(a.class, b.class)
		}
		return strings.Compare(a.worker, b.worker)
	})
	for _, key := range completed {
		labels := fmt.Sprintf("model=\"%s\",class=\"%s\",worker=\"%s\"", label(key.model), label(key.class), label(key.worker))
		fmt.Fprintf(w, "gollm_scheduler_completed_total{%s} %d\n", labels, m.schedulerCompleted[key])
		fmt.Fprintf(w, "gollm_scheduler_completion_failures_total{%s} %d\n", labels, m.schedulerFailed[key])
		fmt.Fprintf(w, "gollm_request_duration_seconds_sum{%s} %g\n", labels, m.requestDurationSum[key])
		fmt.Fprintf(w, "gollm_request_duration_seconds_count{%s} %d\n", labels, m.schedulerCompleted[key])
		fmt.Fprintf(w, "gollm_request_ttft_seconds_sum{%s} %g\n", labels, m.ttftSum[key])
		fmt.Fprintf(w, "gollm_request_ttft_seconds_count{%s} %d\n", labels, m.ttftCount[key])
	}
}

func label(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\n", "\\n")
	return strings.ReplaceAll(value, "\"", "\\\"")
}
