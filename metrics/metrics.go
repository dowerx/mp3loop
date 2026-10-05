package metrics

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Metrics struct {
	PlaysCounter prometheus.Counter
	LoopsCounter prometheus.Counter
	TimeCounter  prometheus.Counter
	ByteCounter  prometheus.Counter

	registry *prometheus.Registry
	server   *http.Server
}

func Make(address string) *Metrics {
	m := &Metrics{
		PlaysCounter: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "mp3loop_plays",
			Help: "Total number of play requests.",
		}),
		LoopsCounter: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "mp3loop_loops",
			Help: "Total number of times listeners have looped around.",
		}),
		TimeCounter: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "mp3loop_served_seconds",
			Help: "Total time of audio served, in seconds.",
		}),
		ByteCounter: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "mp3loop_served_bytes",
			Help: "Total bytes of audio served.",
		}),
	}

	m.registry = prometheus.NewRegistry()
	m.registry.MustRegister(
		m.PlaysCounter,
		m.LoopsCounter,
		m.TimeCounter,
		m.ByteCounter,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	router := gin.New()
	router.Use(gin.Recovery())
	router.GET("/metrics", gin.WrapH(promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})))

	m.server = &http.Server{
		Addr:    address,
		Handler: router,
	}

	return m
}

// ListenAndServe serves the metrics endpoint. It always returns a non-nil error.
func (m *Metrics) ListenAndServe() error {
	return m.server.ListenAndServe()
}

// Shutdown gracefully stops the metrics server.
func (m *Metrics) Shutdown(ctx context.Context) error {
	return m.server.Shutdown(ctx)
}

// Close immediately stops the metrics server.
func (m *Metrics) Close() error {
	return m.server.Close()
}
