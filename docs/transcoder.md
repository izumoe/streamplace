# Live transcoder

The node sends finite MP4 segments to an operator-configured HTTP service for
live video renditions. The included CPU encoder is a separate executable. It
has no node identity, signing keys, database, frontend or VOD processing.
Another implementation can serve the same HTTP contract.

## Build and run

Use the configured native build environment described in
[local development](local-ci.md):

```sh
make transcoder
LD_LIBRARY_PATH=build-linux-amd64/lib build-linux-amd64/transcoder \
  -listen 127.0.0.1:8935 -jobs 2
```

Configure the node with the full endpoint:

```sh
SP_TRANSCODER_URL=http://127.0.0.1:8935/transcode
```

An empty URL disables live transcoding. Existing rendition selection still uses
`SP_TRANSCODE_RENDITIONS`. The node allows two concurrent requests per stream
and skips new work when those slots or the service's slots are busy. Publication
keeps its existing order even when jobs finish out of order.

The listener is private by default and has no application authentication.
Keep it on a private network or place authenticated transport in front of it.
The node treats the endpoint as trusted operator infrastructure.

Each admitted job runs in a child process. Deadlines and cancellation terminate
that process; shutdown waits for active handlers to reap their children. Children
inherit only runtime and native-library environment settings, not application
credentials. The service defaults to two jobs, with a 30-second job deadline.

## HTTP contract

`POST /transcode` accepts `multipart/form-data` with exactly two ordered parts:

1. `profiles`, a JSON array of 1 to 8 objects.
2. `source`, a complete MP4 segment with H.264 video and AAC or Opus audio.

Each profile contains `name`, `width`, `height`, `bitrate` in bits per second,
`profile` (`high` or `baseline`), and optional `fps` and `fpsDen`. For example:

```json
[
  {
    "name": "160p",
    "width": 284,
    "height": 160,
    "bitrate": 250000,
    "profile": "baseline",
    "fps": 30,
    "fpsDen": 1
  }
]
```

Names are unique ASCII letters, digits, underscores or hyphens, up to 64
characters. Dimensions must be even and between 2 and 4096. Bitrates range from
1000 to 100000000. Omit both frame-rate fields to retain the source cadence, or
set both to a positive ratio of at most 240 frames per second.

Success returns `200 multipart/mixed`. Each part has `Content-Type: video/mp4`
and a `Content-Disposition: form-data` name matching one requested profile.
Every requested name must appear exactly once with nonempty media; order may
differ. The client restores ladder order before signing. The included encoder
decodes video once, scales each rendition and encodes H.264 with no B-frames.
It preserves compressed audio packets and does not synthesize extra frames to
meet a higher requested rate.

Malformed requests return 400, oversized source media 413, encoding failures
422, busy admission 503 and job deadlines 504. Failed jobs publish no partial
results. The node signs and verifies successful outputs before adding them to
its live window.

Source media is limited to 64 MiB; profile metadata has an 8 KiB read limit.
The service caps each encoded rendition at 64 MiB and child stdout at 128 MiB;
the client also bounds the response to 128 MiB. These are payload limits, not a
total memory budget: native queues and concurrently buffered renditions need
additional memory. Size `-jobs` and container memory limits for the chosen ladder.

Tests exercise real AAC/Opus segments, frame and packet preservation,
cancellation, admission, malformed output and signed live-window publication.
