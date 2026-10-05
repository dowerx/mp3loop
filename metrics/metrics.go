// Package metrics collects Prometheus counters about served audio and exposes them on a
// scrape endpoint, separate from the stream server so metrics can be bound to a private
// address.
package metrics

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics holds the process counters exposed on the metrics endpoint. The counters are
// exported so callers can record into them directly; all of them are safe for
// concurrent use.
type Metrics struct {
	// PlaysCounter counts requests that successfully began streaming.
	PlaysCounter prometheus.Counter

	// LoopsCounter counts completed passes over the track. A pass is measured relative
	// to where the listener joined, so it fires once the stream returns to the frame it
	// started on rather than at the end of the file. Listeners joining mid-track
	// therefore only count a loop after hearing the whole track.
	LoopsCounter prometheus.Counter

	// TimeCounter accumulates seconds of audio written to clients.
	TimeCounter prometheus.Counter

	// ByteCounter accumulates bytes of audio written to clients.
	ByteCounter prometheus.Counter

	registry *prometheus.Registry
	server   *http.Server
}

// Make creates the counters and the server that exposes them at /metrics, bound to
// address. Go runtime and process collectors are registered alongside the audio
// counters. The returned server is not yet listening; use ListenAndServe and Shutdown.
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

// ListenAndServe starts the metrics server and blocks until it stops. It always
// returns a non-nil error, and returns http.ErrServerClosed after a Shutdown or Close.
func (m *Metrics) ListenAndServe() error {
	return m.server.ListenAndServe()
}

// Shutdown gracefully stops the metrics server, waiting for in-flight scrapes to finish
// until ctx is done. It returns ctx.Err() if the timeout expires first.
func (m *Metrics) Shutdown(ctx context.Context) error {
	return m.server.Shutdown(ctx)
}

// Close immediately stops the metrics server, dropping any in-flight scrapes. Use it
// as the fallback when Shutdown fails.
func (m *Metrics) Close() error {
	return m.server.Close()
}
