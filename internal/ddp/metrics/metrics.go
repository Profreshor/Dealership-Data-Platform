// Package metrics exposes bounded Prometheus observations on a private listener.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const APIAddr = "127.0.0.1:9091"
const SchedulerAddr = "127.0.0.1:9092"

type Metrics struct {
	registry *prometheus.Registry
	duration *prometheus.HistogramVec
	patterns map[string]bool
}

func New(pool *pgxpool.Pool, cfg *config.Config, patterns []string) *Metrics {
	m := &Metrics{registry: prometheus.NewRegistry(), patterns: map[string]bool{}}
	for _, pattern := range patterns {
		if pattern != "" {
			m.patterns[pattern] = true
		}
	}
	m.duration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "ddp_http_request_duration_seconds", Help: "HTTP request duration by registered route pattern, bounded method and response status.",
		Buckets: prometheus.DefBuckets,
	}, []string{"route", "method", "status"})
	m.registry.MustRegister(m.duration, collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}), NewDatabase(pool, cfg))
	return m
}

// ObserveHTTP accepts only the route patterns captured when this process started.
func (m *Metrics) ObserveHTTP(pattern, method string, status int, elapsed time.Duration) {
	if !m.patterns[pattern] {
		pattern = "unmatched"
	}
	switch method {
	case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "CONNECT", "OPTIONS", "TRACE":
	default:
		method = "OTHER"
	}
	code := "other"
	if status == 0 {
		code = "aborted"
	} else if status >= 100 && status <= 599 {
		code = strconv.Itoa(status)
	}
	m.duration.WithLabelValues(pattern, method, code).Observe(max(0, elapsed.Seconds()))
}

func (m *Metrics) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{
		Timeout: 5 * time.Second, MaxRequestsInFlight: 1,
	}))
	return mux
}
