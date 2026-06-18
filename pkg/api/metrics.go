package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Prometheus metrics. Registered on the default registry at package load, so
// they are shared across Server instances (counters accumulate).
var (
	httpRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "lb_http_requests_total",
		Help: "Total HTTP requests by route, method, and status code.",
	}, []string{"route", "method", "code"})

	httpDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "lb_http_request_duration_seconds",
		Help:    "HTTP request latency by route and method.",
		Buckets: []float64{0.0001, 0.00025, 0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1},
	}, []string{"route", "method"})

	submitsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "lb_submits_total",
		Help: "Score submissions by outcome (accepted, duplicate, unknown_board, rejected, error).",
	}, []string{"result"})

	consumerApplied = promauto.NewCounter(prometheus.CounterOpts{
		Name: "lb_consumer_records_applied_total",
		Help: "Records applied to the engine by the ingest consumer.",
	})
)

// RecordConsumerApplied is the hook the ingest consumer calls (wired in main)
// so consumer throughput is observable without coupling the ingest package to
// Prometheus.
func RecordConsumerApplied(n int) { consumerApplied.Add(float64(n)) }

// statusWriter captures the response status code for metrics.
type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(c int) {
	w.code = c
	w.ResponseWriter.WriteHeader(c)
}

// routeLabel strips the leading "METHOD " from a ServeMux pattern.
func routeLabel(pattern string) string {
	if i := strings.IndexByte(pattern, ' '); i >= 0 {
		return pattern[i+1:]
	}
	return pattern
}

// instrument wraps a handler to record request count and latency under a fixed
// route label.
func instrument(route string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w, code: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(sw, r)
		httpDuration.WithLabelValues(route, r.Method).Observe(time.Since(start).Seconds())
		httpRequests.WithLabelValues(route, r.Method, strconv.Itoa(sw.code)).Inc()
	})
}
