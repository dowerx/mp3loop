# mp3loop

Serves an MP3 over HTTP as an endless, wall-clock-synchronized stream.

Every listener who connects at a given moment hears the track from the same point at
the same moment, and the stream never ends — the file loops for as long as the client
stays connected. There is no playhead to negotiate and nothing to seek: the server
decides where the music is, clients just listen.

```
go build -o mp3loop .
./mp3loop -f /path/to/your/file.mp3
```

MP3s are gitignored, so bring your own. Then point anything that speaks HTTP at it:

```sh
ffplay http://localhost:8080/stream.mp3        # or just open it in a browser
```

## How the sync works

Playback position is derived from the wall clock, not from any client. On each request
the server converts the current time to a sample offset in the track and starts there,
so a listener who connects three minutes into the song joins the part being played
rather than restarting it.

Frames are served one at a time and written after sleeping for that frame's playback
duration, so the connection carries data at the rate the track plays. The stream keeps
wrapping at the end of the file, which means a client that stays connected for an hour
receives an hour of audio in roughly the right order.

The file is memory mapped and its MPEG frames indexed once at startup, so serving is
essentially free per listener and many clients can share one mapped file with no locking.

## Flags

| Flag | Default             | Description                          |
| ---- | ------------------- | ------------------------------------ |
| `-f` | `./audio.mp3`       | Path to the mp3 to be played         |
| `-l` | `:8080`             | Stream listen address                |
| `-u` | `/stream.mp3`       | URI to serve the stream at           |
| `-m` | `:9080`             | Prometheus metrics listen address    |

Both listen addresses default to all interfaces. Metrics are unauthenticated, so bind
`-m` to `127.0.0.1:9080` if the metrics port should not be reachable externally.

## Endpoints

- `GET /stream.mp3` — the audio stream. Long lived, loops indefinitely, ends when the
  client disconnects. Served as `audio/mpeg` with caching disabled.
- `GET /metrics` — Prometheus exposition format, on the metrics port.

## Metrics

| Metric                   | Type    | Description                                       |
| ------------------------ | ------- | ------------------------------------------------- |
| `mp3loop_plays`          | counter | Requests that successfully began streaming        |
| `mp3loop_loops`          | counter | Completed passes over the track                   |
| `mp3loop_served_seconds` | counter | Seconds of audio written to clients               |
| `mp3loop_served_bytes`   | counter | Bytes of audio written to clients                 |

Standard `go_*` and `process_*` collectors are registered alongside these.

`mp3loop_loops` counts full passes, and a pass is measured relative to where each
listener joined rather than at the end of the file. A listener who connects mid-track
is only counted once they have heard the whole thing, so the counter reflects loops
actually experienced rather than file-end crossings. The consequence is that it is a
per-listener count: many short-lived connections can each contribute little, while a
single long-lived one contributes steadily.

## Docker

```sh
docker build -t mp3loop .
docker run --rm -p 8080:8080 -p 9080:9080 \
    -v "$PWD/your.mp3:/audio/your.mp3:ro" \
    mp3loop -f /audio/your.mp3
```

The image is built `FROM scratch` and contains only the server binary, so the MP3 has to
be mounted in rather than baked in. Note that the container ships no CA certificates or
shell. Images are published to `ghcr.io` on pushes to `master` and on `v*` tags.

## Development

Requires Go 1.26. Does not build on Windows — the audio package maps files with
`golang.org/x/sys/unix`.

```sh
go build ./...
go test ./... -race
go vet ./...
```

The code is split so each layer can be read on its own:

- `audio` — MP3 parsing, frame indexing, and streaming. Depends only on the standard
  library and `x/sys`, so it can be reused without pulling in a web framework. It
  reports loops through an `onLoop` callback rather than depending on the metrics
  package.
- `api` — the HTTP handler that paces the stream and records counters.
- `metrics` — counter definitions and the scrape endpoint.

## Notes and limitations

- **MPEG Layer III only.** MPEG-1, MPEG-2 and MPEG-2.5 are all handled, but frames
  from any other layer are skipped during indexing, so AAC, FLAC or exotic files fail to
  load. Load logs the frame count; zero frames is an error.
- **Constant bitrate assumed.** Frame duration is derived from a per-frame sample rate,
  which is correct for CBR and approximate for VBR files.
- **No transcoding.** Frames are streamed as-is; whatever the file contains is what
  clients receive.
- **One track per process.** Run separate instances for separate tracks, each with its
  own `-l` and `-m`.
- **Unbounded connections.** There is no listener limit or auth on the stream endpoint.
  Put it behind a reverse proxy if it needs to be public.