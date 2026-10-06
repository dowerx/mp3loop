// Package api serves the audio stream over HTTP.
//
// A single long lived request streams the track in real time, paced so the client hears
// it at normal speed, and loops indefinitely until the client disconnects.
package api

import (
	"log/slog"
	"mp3loop/audio"
	"mp3loop/metrics"
	"net/http"
	"time"

	gslog "github.com/gin-contrib/slog"

	"github.com/gin-gonic/gin"
)

// Make builds an http.Server that serves the audio stream at uri, recording counters
// into metrics as frames are written. The returned server is not yet listening; the
// caller is responsible for calling ListenAndServe and shutting it down.
func Make(
	address string,
	uri string,
	audio audio.Audio,
	metrics *metrics.Metrics) *http.Server {
	router := gin.New()
	router.Use(gslog.SetLogger(gslog.WithLogger(
		func(c *gin.Context, l *slog.Logger) *slog.Logger {
			return slog.Default()
		})))
	router.Use(gin.Recovery())
	router.GET(uri, streamFactory(audio, metrics))

	return &http.Server{
		Addr:    address,
		Handler: router,
	}
}

// streamFactory returns a handler that streams audio in real time, pacing writes so
// the listener hears the track at its original speed. Playback begins at the sample
// matching the wall clock, so every listener joining at a given moment hears the same
// part of the track. The response stays open indefinitely as the track loops, and ends
// only when the client disconnects.
func streamFactory(audio audio.Audio, m *metrics.Metrics) gin.HandlerFunc {
	return func(c *gin.Context) {
		sample := audio.SampleAt(time.Now())
		ch, err := audio.StreamFromSample(c, int64(sample), true, m.LoopsCounter.Inc)
		if err != nil {
			slog.Error("error while streaming", "err", err)
			c.JSON(400, gin.H{"err": err})
			return
		}

		m.PlaysCounter.Inc()

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
			m.TimeCounter.Add(chunk.Duration.Seconds())
			m.ByteCounter.Add(float64(len(chunk.Data)))

			nextWrite = nextWrite.Add(chunk.Duration)
		}
	}
}
