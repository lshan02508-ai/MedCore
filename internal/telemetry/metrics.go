package telemetry

import (
	"fmt"
	"net/http"
	"sync/atomic"
)

type Metrics struct {
	requests atomic.Uint64
	inflight atomic.Int64
	errors   atomic.Uint64
}

func (m *Metrics) Begin() func(bool) {
	m.requests.Add(1)
	m.inflight.Add(1)
	return func(failed bool) {
		m.inflight.Add(-1)
		if failed { m.errors.Add(1) }
	}
}

func (m *Metrics) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	fmt.Fprintf(w, "# TYPE gollm_requests_total counter\ngollm_requests_total %d\n", m.requests.Load())
	fmt.Fprintf(w, "# TYPE gollm_requests_inflight gauge\ngollm_requests_inflight %d\n", m.inflight.Load())
	fmt.Fprintf(w, "# TYPE gollm_proxy_errors_total counter\ngollm_proxy_errors_total %d\n", m.errors.Load())
}
