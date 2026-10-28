# Media over QUIC

Streamplace uses Media over QUIC for live video between nodes and browser
playback. Nodes use `moq.dev/moq`, the official Go wrapper over the
Rust `moq-ffi` that Meson builds from source into the node (see
`rust/streamplace-native`). `pkg/moq` holds only what Streamplace adds, the
listener's certificate handling. The listener and the pull side live in the
`websocketrep` replicator (`pkg/replication/websocketrep/moq.go`), which
prefers an origin's MoQ URL over its websocket when one is advertised. The
browser uses `@moq/watch`.

## Wire convention

Every byte a broadcast carries is signed MUXL, laid out the way the rest of
the MoQ ecosystem reads media: a hang catalog and one CMAF track per media
track. A Streamplace node reassembles the exact signed segments and verifies
them as before. Browser playback uses the same CMAF format, with the timing
section described below for signed media batches.

| MoQ                                         | Streamplace                                                                                                 |
| ------------------------------------------- | ----------------------------------------------------------------------------------------------------------- |
| broadcast path                              | the streamer's DID (`did:plc:...`) for peers; `playback/did:plc:...` for browser playback                   |
| `catalog.json`, `catalog.json.z`, `catalog` | the hang and MSF catalogs, one CMAF rendition per track                                                     |
| track                                       | one MUXL track, named by its MUXL rendition (`video1`, `audio2`, `audio3`; renditions `video100` up)        |
| group                                       | that track's canonical segment for one GoP                                                                  |
| frame                                       | one `moof`+`mdat`, the first also carrying the segment's leading `uuid` boxes (C2PA manifest, MUXL catalog) |
| timestamp                                   | the fragment's first presentation time, on the track's timescale                                            |

A group's frames concatenate back to the track's canonical segment, byte
for byte. Each catalog rendition's CMAF `init` is the track's own init
segment, synthesized from its MUXL catalog (`muxl wrap --init-only`); the
hang codec fields are derived from that init by `moq-mux`. The catalog is
unsigned: a node uses it only to find the tracks, and verifies the bytes.

The source segment's tracks (MUXL track ids below
`media.RenditionTrackID(0)`) share one group sequence per segment. A
subscriber reads each in sequence order, concatenates one segment's
groups in track-id order, and validates the result with
`MediaManager.ValidateReplicatedMP4`: the websocket path's validation,
plus a check that the segment's signed creator is the streamer it asked
for. A source track with no group at a segment's sequence has no part in
that segment. Each rendition track's group is a single-track addendum,
verified with `muxl verify` and folded into the live window on its own.
Anything that is not MUXL ends the session.

A node's listener advertises one route covering every path and serves a
broadcast when a peer requests it: a request for a stream that is not live
on its segment bus is refused, and a served broadcast ends with the
stream's session, or when a track's codec config changes (its MUXL catalog
box differs), since the catalog entry describes the old one; the next
request starts afresh. The transport shares one served broadcast among all
its subscribers. A track is published the first time a segment carries it,
and a new subscriber starts from the newest segment, like
`place.stream.live.subscribeSegments`'s priming. The source tracks have the
higher subscriber priority, and a subscriber that falls behind skips ahead.

To watch or dump a broadcast with the MoQ CLI (pin a self-signed node's
certificate with the `certhash` from its MoQ URL):

```sh
moq --connect moqt://host:38443 --connect-tls-fingerprint <certhash> \
    --broadcast did:plc:... export fmp4 > stream.mp4
```

## Bindings

One UDP port serves raw QUIC (`moqt://host:port`) and WebTransport, which
is the binding a browser uses. The transport negotiates the protocol
version by ALPN.

## Web playback

The DOM player's low-latency mode tries MoQ in a secure browser with
WebTransport, WebCodecs and Web Audio support. It falls back to WebRTC and
then HLS when discovery or playback fails. Standard mode and VOD use HLS.
An explicit `webrtc://` source keeps its WebRTC selection.

`place.stream.playback.getPlaybackServer?stream=<handle-or-DID>` returns
the existing `servers` list and, when an origin refreshed within the last
minute advertises MoQ, an optional `moq` object:

```json
{
  "moq": {
    "url": "moqt://origin.example:38443?certhash=<SHA-256-hex>",
    "name": "playback/did:plc:..."
  }
}
```

The player converts the transport URL to HTTPS for WebTransport and pins
the advertised certificate hash when present. The origin's UDP listener
must be reachable by the browser; an HTTP reverse proxy alone does not
carry this connection. Discovery uses the configured node, independently
of whether Bluesky's public AppView has indexed the streamer's profile.

The playback broadcast uses independently verified source media before
the ingest worker finishes adding AAC. Peers continue to receive the
completed, signed canonical media on the ordinary DID path. Signature,
streamer identity and distribution checks apply before source playback;
unpublished segments are not served over MoQ. When a live bitrate limit
requires admission of completed media, playback waits for that admission.

Signed segments arrive as whole GoP batches. The playback catalog carries
`streamplace.batchDurationMs`, the rounded-up presentation span of a batch,
including track offsets and final sample durations. It grows to the largest
batch observed by that broadcast and resets for a new broadcast. The
publisher sends this section before making the first media reachable.

The pinned `@moq/watch` player initially registers the batch duration plus a
200 ms scheduling reserve with its existing sync mechanism. Audio starts after
the buffer covers this floor. Small timing increases consume the available
reserve without resizing the audio buffer. A batch that outgrows the floor
raises it to the new duration plus reserve and can require rebuffering. The
reserve is spare capacity, so it does not guarantee a constant 200 ms margin
under changing cadence. Network buffering remains automatic, and the same floor
applies to audio and video. Missing or invalid timing, or a selected floor that
reaches the player's ten-second progress budget, selects the existing fallback
transports. Signed GoP delivery still bounds latency.

MoQ renders to a canvas and Web Audio, using the shared playback controls
for pause, volume, quality and fullscreen. Picture-in-picture is available
on video-element transports. Switching transports or leaving playback
closes the MoQ player, audio contexts and connection.

## Measuring live latency

Inside the configured [Docker builder](../.claude/skills/streamplace-docker/SKILL.md),
build a fresh embedded frontend and node, then run:

```sh
make app
make dev
E2E_LATENCY=1 hack/e2e-web-local.sh --project=chromium flows/live-latency.spec.ts
```

The ordinary harness supplies a temporary account and isolated services. This
mode exports that account's stream key to the test instead of starting the
fixture publisher. The browser publishes a real 640 by 360, 30 fps H.264/Opus
WHIP stream, with a checked frame identifier and a continuous audio tone. It
observes the DOM MoQ player and a native WHEP receiver on the publisher's
monotonic clock. Traces and video recording are disabled to keep the temporary
stream key out of artifacts.

After ten seconds of settling, the test records twenty seconds of frame ages,
new-frame gaps, RTC counters and actual speaker PCM. It also records native
audio-worklet cursor reports continuously, rejecting parked or nonadvancing
output during steady playback. It rejects an unhealthy source, frozen viewers
or missing audio. `live-latency.json` in the Playwright output retains raw
observations and summaries. Repeated canvas observations do not count as new
frames, and video callback counts are distinct from decoder throughput.

For comparisons, `E2E_BUILDDIR` selects a frozen build directory containing the
launcher, binary and compatible native libraries. Set `E2E_REVISION` and
`E2E_BINARY_SHA256` to record its provenance. Keep the measurement source and
libraries identical, run one experiment at a time, and alternate build order.
Compare each transport with itself: MoQ's endpoint is a parent animation-frame
observation of its canvas; WHEP's is the native video's expected presentation
time. These measure generated-frame age, with different display endpoints,
rather than camera capture or physical screen latency.

The node requests WHIP keyframes through one 400 ms PLI scheduler. Publishers
can respond differently, and shorter GoPs increase keyframe bitrate and
signing/validation job frequency. Signed GoP delivery still bounds latency.
Muxl's existing shared engine initializes before node readiness and before a
WHIP worker answers SDP, moving cold compilation ahead of media arrival. It
does not reduce compilation CPU or remove worker isolation.

WebRTC subscribes when its connection is established and starts from the newest
cached GoP. Each track keeps a deadline across queued GoPs, so alternating audio
and video tails do not accumulate a wait at every boundary. Empty-input waits
shift both deadlines to preserve paced recovery after a source stall. RTP
durations and the existing backlog rate policy remain unchanged.

On October 28, 2026, two baseline and two candidate runs used Chromium
156.0.8078.4 in the same Linux amd64 builder, emulated on an ARM Mac, using
postMessage audio with cross-origin isolation disabled. Run order was candidate,
baseline, baseline, candidate. Each cell below is the range of
per-run results, rather than a pooled distribution of correlated frames.

| Measurement                                     | Baseline           | Candidate         |
| ----------------------------------------------- | ------------------ | ----------------- |
| MoQ median frame age                            | 1193 to 1458 ms    | 843 to 987 ms     |
| WHEP median frame age                           | 2143 to 2537 ms    | 578 to 934 ms     |
| WHEP second-half median minus first-half median | 73 to 223 ms       | 6 to 10 ms        |
| WHEP frame-age slope                            | 7.04 to 22.75 ms/s | 0.76 to 0.92 ms/s |

All four runs passed the source and video health checks. Both candidate runs
also passed the full audio checks. The second baseline run failed audio
continuity: two of twenty PCM snapshots were silent, with 121 parked and 119
nonadvancing native cursor reports. Its video observations remain in the table;
it is not a passing playback run. The failure was retained without rerunning
or weakening the acceptance criteria.

A separate 120-second candidate run kept all 9,000 native audio cursor reports
advancing, with no parked output, and all 120 speaker PCM snapshots audible.
WHEP decoded about 30 video frames per second, with no added packet loss, video
drops, audio concealment or sample insertion/removal during steady playback.
Its first and last twenty-second median frame ages were 976.3 and 979.5 ms;
source-to-receiver delay changed by about 0.3 ms. These checks cover minutes on
one builder, so deployment hardware and longer sessions need separate measurements.

A one-second-GoP fixture check compared the new native pipeline with and without
the browser reserve policy. Steady median speaker lag, estimated from the native
worklet cursor and reported output latency, was about 16 ms with the policy and
27 ms without it. The new player had no offsets over 100 ms in the observed
first-audible twenty-second window. The comparison build had brief startup lag
up to 204 ms across its first 698 ms. This one pair does not establish a general
join-time bound. Decoder-input-to-canvas delay increased by about 206 ms for
this fixture, consistent with the initial 200 ms reserve.

The baseline production source was `73ed3d5b2` with the external-publisher test
harness added. Frozen binary SHA-256 values were
`a8b2f495f9bf8931199a43f1afa9dc998b26d1b866764e5cfa56939f858e77bc`
(baseline) and
`a4144a06ffd22d79e2063ef2cda5770aff4639f289aaa17bee8285cd4bfdfe48`
(candidate). Shared native library hashes and measurement source were identical.

## Configuration

| Flag         | Env           | Default  | Meaning                                                                                      |
| ------------ | ------------- | -------- | -------------------------------------------------------------------------------------------- |
| `--moq-addr` | `SP_MOQ_ADDR` | `:38443` | UDP listen address; empty disables the listener.                                             |
| `--moq-url`  | `SP_MOQ_URL`  |          | Override for the advertised `moqt://` URL (default: `--server-host` on `--moq-addr`'s port). |

The listener terminates TLS itself, even on a node behind an HTTPS proxy,
reading its certificate from files (the data directory's `moq-tls`) that
Go keeps current:

- with `--secure`, it serves the node's certificate (ACME, rewritten as it
  renews, or `--tls-cert`/`--tls-key` as given);
- otherwise it mints a self-signed ECDSA certificate, valid for two weeks
  and rotated weekly (the limits a browser puts on a pinned certificate),
  and advertises its SHA-256 as the `certhash` query parameter of the MoQ
  URL. A subscriber pins exactly that certificate.

## Discovery

The advertised URL travels two ways, like the websocket URL:

- the `moqURL` field of the streamer's `place.stream.broadcast.origin`
  record on the firehose;
- the `moq_url` column of the station's shared `broadcast_origins` table,
  written by the ingest node on every local segment, so nodes sharing a
  statedb find each other without a relay.
