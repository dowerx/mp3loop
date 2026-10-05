package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"mp3loop/audio"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	gslog "github.com/gin-contrib/slog"
	"github.com/gin-gonic/gin"
)

func main() {
	// setup logging
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	// parse arguments
	path := flag.String("f", "./audio.mp3", "path to the mp3 to be played")
	address := flag.String("l", ":8080", "listen address")
	uri := flag.String("u", "/stream.mp3", "uri to serve the stream at")
	flag.Parse()

	// load audio
	data := audio.Audio{}
	if err := data.Load(*path); err != nil {
		slog.Error("failed to load audio", "err", err)
		os.Exit(1)
	}
	defer data.Close()

	// setup webserver
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gslog.SetLogger())
	r.Use(gin.Recovery())

	r.GET(*uri, func(c *gin.Context) {
		sample := data.SampleAt(time.Now())
		ch, err := data.StreamFromSample(c, int64(sample), true)
		if err != nil {
			slog.Error("error while streaming", "err", err)
			c.JSON(400, gin.H{"err": err})
			return
		}

		c.Header("Content-Type", "audio/mpeg")
		c.Header("Cache-Control", "no-cache")
		nextWrite := time.Now()

		for chunk := range ch {
			wait := time.Until(nextWrite)
			if wait > 0 {
				timer := time.NewTimer(wait)
				select {
				case <-c.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}

			if _, err := c.Writer.Write(chunk.Data); err != nil {
				return
			}
			c.Writer.Flush()

			nextWrite = nextWrite.Add(chunk.Duration)
		}

	})

	// serve
	server := http.Server{
		Addr:    *address,
		Handler: r,
	}

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- server.ListenAndServe()
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
	case sig := <-quit:
		slog.Info("shutting down", "signal", sig.String())
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			slog.Error("graceful shutdown failed", "err", err)
			if err := server.Close(); err != nil {
				slog.Error("server close failed", "err", err)
			}
		}
	}
}
