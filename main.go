package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"mp3loop/api"
	"mp3loop/audio"
	"mp3loop/metrics"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
)

const shutdownTimeout = 5 * time.Second

func main() {
	// setup logging
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	// parse arguments
	path := flag.String("f", "./audio.mp3", "path to the mp3 to be played")
	address := flag.String("l", ":8080", "listen address")
	uri := flag.String("u", "/stream.mp3", "uri to serve the stream at")
	metricsAddress := flag.String("m", ":9080", "listen address for prometheus metrics")
	flag.Parse()

	gin.SetMode(gin.ReleaseMode)

	// serve metrics
	m := metrics.Make(*metricsAddress)
	metricsErr := make(chan error, 1)
	go func() {
		metricsErr <- m.ListenAndServe()
	}()

	// load audio
	a := audio.Audio{}
	if err := a.Load(*path); err != nil {
		slog.Error("failed to load audio", "err", err)
		os.Exit(1)
	}
	defer a.Close()

	// serve stream
	srv := api.Make(*address, *uri, a, m)
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- srv.ListenAndServe()
	}()

	// graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(quit)

	select {
	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			slog.Error("error while serving", "err", err)
			os.Exit(1)
		}

	case err := <-metricsErr:
		if !errors.Is(err, http.ErrServerClosed) {
			slog.Error("error while serving metrics", "err", err)
			os.Exit(1)
		}

	case sig := <-quit:
		slog.Info("shutting down", "signal", sig.String())

		shutdown := func(
			name string,
			s interface {
				Shutdown(context.Context) error
				Close() error
			}) {
			ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
			defer cancel()

			if err := s.Shutdown(ctx); err != nil {
				slog.Error("graceful shutdown failed", "server", name, "err", err)
				if err := s.Close(); err != nil {
					slog.Error("server close failed", "server", name, "err", err)
				}
			}
		}

		shutdown("stream", srv)
		shutdown("metrics", m)
	}
}
