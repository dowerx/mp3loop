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

func Make(
	address string,
	uri string,
	audio audio.Audio,
	metrics *metrics.Metrics) *http.Server {
	router := gin.New()
	router.Use(gslog.SetLogger())
	router.Use(gin.Recovery())
	router.GET(uri, streamFactory(audio, metrics))

	return &http.Server{
		Addr:    address,
		Handler: router,
	}
}

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
