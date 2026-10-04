package main

import (
	"flag"
	"mp3loop/audio"
	"time"

	"github.com/gin-gonic/gin"
)

func main() {
	path := flag.String("f", "./audio.mp3", "path to the mp3 to be played")
	address := flag.String("l", ":8080", "listen address")

	flag.Parse()

	data := audio.Audio{}
	if err := data.Load(*path); err != nil {
		panic(err)
	}

	r := gin.Default()
	r.GET("/stream.mp3", func(c *gin.Context) {
		sample := data.SampleAt(time.Now())
		ch, err := data.StreamFromSample(c, int64(sample), true)
		if err != nil {
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

	if err := r.Run(*address); err != nil {
		panic(err)
	}
}
