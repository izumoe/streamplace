# MoQ for developers

Streamplace uses Media over QUIC (MoQ) to send live media between nodes and to
browsers. It uses the moq-dev transport and hang catalog format, with
Streamplace's signed MUXL media carried as CMAF tracks.

This guide covers the media flow, verification, deployment requirements and
places to start reading the code.

## Follow a stream

1. The ingest node signs the source media as MUXL segments. A segment contains
   encoded audio or video, plus the metadata needed to verify its provenance.
2. The node publishes those segments as MoQ tracks. CMAF is the fragmented MP4
   format used to carry the media; the hang catalog describes the tracks and
   their codec configuration.
3. Another node can subscribe, reconstruct the signed segments and verify them.
   A browser can subscribe to the playback broadcast and decode the media.

There are two broadcast paths for a stream:

| Subscriber   | Path                   | Media                                                      |
| ------------ | ---------------------- | ---------------------------------------------------------- |
| Another node | `did:plc:...`          | Completed, signed segments, including available renditions |
| Browser      | `playback/did:plc:...` | Verified source media grouped for playback                 |

Worker-based ingest can publish verified source media for playback before audio
completion adds AAC. Direct RTMP ingest publishes after source GoP signing and
audio completion. If a live bitrate limit requires admission of completed media,
playback waits for that admission. Unpublished media is not served.

## What gets verified

The media bytes retain their MUXL signatures in transit. A receiving node
reassembles the signed segments, verifies them and checks that their creator is
the requested streamer. Signature, streamer identity and distribution checks
also apply before the origin serves source playback.

The browser player decodes the media without independently verifying MUXL
signatures. It relies on the serving node's verification. The catalog itself is
unsigned and serves as a track directory; receiving nodes verify the media
rather than trusting catalog claims.

QUIC encrypts the connection. MUXL signatures establish media provenance and
integrity independently of that connection.

## What comes from moq-dev

| Component               | Implementation                                                                         |
| ----------------------- | -------------------------------------------------------------------------------------- |
| Node transport          | `moq.dev/moq`, a Go wrapper over Rust `moq-ffi`                                        |
| Native build            | Meson builds the Rust transport into `rust/streamplace-native`                         |
| Browser player          | `@moq/watch`, using a hang catalog, WebTransport, WebCodecs and Web Audio              |
| Streamplace integration | Signed-media publishing and verification, discovery, certificates and browser controls |

The node pins Streamplace's transport fork for CMAF passthrough support. Keep
its Go module replacements in `go.mod` and the native revision in
`subprojects/moq.wrap` in step when updating it. The browser package also has a
quality-selection patch, described in the maintenance appendix below.

The native build also patches CMAF bitrate measurement and Noq's bandwidth
reporting. `subprojects/moq-noq.wrap` pins the source matching the existing Noq
release. Keep all four Cargo overrides in the transport's source patch on that
same checkout when updating it.

`make native-source` refreshes cached sources when their Git pin or Meson wrap
hash changes, using Meson's existing reset/stash mechanism. The marker hashes
the wrap text, not patch contents. When editing a patch at an existing path,
also update its wrap definition or explicitly reapply it with
`meson subprojects update --reset --sourcedir . moq` (or `moq-noq`).

## Run a reachable listener

One UDP port serves both raw QUIC (`moqt://`) and browser WebTransport. The
connection negotiates its protocol version through ALPN.

| Flag         | Environment   | Default                                          | Purpose                                                  |
| ------------ | ------------- | ------------------------------------------------ | -------------------------------------------------------- |
| `--moq-addr` | `SP_MOQ_ADDR` | `:38443`                                         | UDP listen address; an empty value disables the listener |
| `--moq-url`  | `SP_MOQ_URL`  | Derived from `--server-host` and the listen port | Public `moqt://` URL advertised to subscribers           |

Make the advertised host and UDP port reachable from subscribers. An HTTPS
reverse proxy alone does not carry the MoQ connection. The MoQ listener
terminates TLS itself, including when the node's HTTP server sits behind a proxy.

The node maintains certificate files in the data directory's `moq-tls`:

- With `--secure`, it uses the node's ACME certificate or the configured
  `--tls-cert` and `--tls-key`. ACME renewals update the files.
- Otherwise, it creates an ECDSA certificate valid for two weeks and rotates it
  weekly. Its advertised URL includes a SHA-256 `certhash` that subscribers pin.

The origin advertises its URL in the streamer's `place.stream.broadcast.origin`
record as `moqURL`. It also updates `broadcast_origins.moq_url` on each local
segment, allowing nodes that share a station database to discover it without
waiting for the firehose. Peer replication prefers an advertised MoQ URL over
the origin's websocket URL.

To inspect a peer broadcast with the MoQ CLI, use the advertised certificate
hash for a self-signed node:

```sh
moq --connect moqt://host:38443 --connect-tls-fingerprint <certhash> \
    --broadcast did:plc:... export fmp4 > stream.mp4
```

## Browser playback

The DOM app's low-latency mode tries MoQ when the browser has a secure context,
WebTransport, WebCodecs and Web Audio. If discovery or playback fails, it falls
back to WebRTC, then HLS. Standard mode and VOD use HLS. An explicit `webrtc://`
source selects WebRTC directly.

The player asks the configured node for
`place.stream.playback.getPlaybackServer?stream=<handle-or-DID>`. Alongside the
usual `servers` list, the response can include:

```json
{
  "moq": {
    "url": "moqt://origin.example:38443?certhash=<SHA-256-hex>",
    "name": "playback/did:plc:..."
  }
}
```

MoQ discovery requires an origin advertisement refreshed within the last minute.
It does not require Bluesky's public AppView to have indexed the profile. The
player converts the URL to HTTPS for WebTransport and pins `certhash` when
present.

MoQ draws video to a canvas and plays audio through Web Audio. The app connects
pause, volume, quality and fullscreen controls to that player. Picture-in-picture
is available on the video-element transports. Leaving playback or switching
transports closes the player, audio contexts and connection.

### Bitrates and automatic quality

Each CMAF track advertises its measured `bitrate` in the standard catalog field.
The existing native estimator counts complete fragments, including C2PA and
container headers, against decode timestamps. It averages completed media spans
covering at least one second and retains the highest observed average for the
track's lifetime. Bursty signing or delivery does not change that media-time
rate. The field remains absent until enough media has been measured. Nominal
codec-descriptor rates do not override this measurement; the signed media and
initialization bytes remain unchanged.

These rates exclude QUIC, TLS, UDP and IP overhead. The player's **Video bitrate
estimate** shows the selected rendition's catalog cost, not current network
throughput. **Bandwidth estimate** shows the peer's connection estimate. The
default Noq BBR transport reports its delivery-rate model without pacing or
congestion-window gains. Controllers without that model, including Cubic and
Reno, report no estimate.

Auto reserves the selected, downloaded audio track's measured rate from 80% of
the connection estimate, then chooses the best resolution satisfying the video budget and any
dimension limits. An efficient higher-resolution AV1 rendition can therefore
beat a more expensive lower-resolution H.264 rendition. If every known rate
exceeds the budget, it falls back to the cheapest rendition. Without an estimate
or measured rates, it uses the existing quality preference. Explicit video
budgets and manual track choices retain their meaning.

During a handoff, pending video has higher delivery priority than outgoing
video, while audio stays above both. Normal video priority resumes on promotion.
Returning to the already active rendition cancels pending work and reuses its
decoder. Track name and codec configuration are selected together.

### Buffering and latency

A signed segment arrives as a whole group of pictures (GoP), so its duration
sets a lower bound on delivery latency. MoQ cannot deliver frames that the
publisher has not yet signed and released. The node requests WHIP keyframes
through a 400 ms PLI scheduler; the publisher determines how it responds.
Shorter GoPs increase keyframe bitrate and signing/validation work.

The playback catalog includes `streamplace.batchDurationMs`: the rounded-up
presentation span of a signed batch, including track offsets and final sample
durations. The publisher sends it before exposing the first media. It increases
to the largest batch observed in the broadcast and resets for a new broadcast.

The player registers an initial buffering floor of the batch duration plus
200 ms with its existing sync mechanism. Audio starts when the buffer covers
that floor. Small duration increases consume the reserve; a batch that outgrows
the floor raises it and can trigger rebuffering. Network buffering remains
automatic, with the same floor for audio and video. The reserve does not promise
a constant 200 ms margin as source cadence changes.

Missing or invalid timing, or a buffering floor that reaches the player's
ten-second progress budget, triggers fallback to the other transports.

## Track layout and peer replication

Use this section when changing the publisher or subscriber. A MoQ frame here is
a transport object containing a CMAF fragment, not necessarily one video frame.

| MoQ object     | Contents                                                                                                       |
| -------------- | -------------------------------------------------------------------------------------------------------------- |
| Catalog        | Hang (`catalog.json`, `catalog.json.z`) and MSF (`catalog`) descriptions, with one CMAF rendition per track    |
| Track          | One MUXL track, named by rendition: `video1`, `audio2`, `audio3`; generated renditions start at `video100`     |
| Peer group     | A track's contribution to a shared admission-batch sequence                                                    |
| Playback group | A video GoP or an audio batch                                                                                  |
| Frame          | One `moof` + `mdat`; the first also carries leading `uuid` boxes containing the C2PA manifest and MUXL catalog |
| Timestamp      | The fragment's first presentation time, in the track's timescale                                               |

Each track's CMAF initialization segment is synthesized from its MUXL catalog
with `muxl wrap --init-only`. `moq-mux` derives the hang codec fields from that
initialization segment. Browser video groups start at actual sync samples and
can cross admission batches; audio groups follow the batches.

Peer groups preserve each track's signed canonical assets byte for byte. Source
tracks, whose IDs are below `media.RenditionTrackID(0)`, share a group sequence
per segment. The subscriber reads them in sequence, joins a segment's groups in
track-ID order and calls `MediaManager.ValidateReplicatedMP4`. A source track
with no group at that sequence contributes nothing to that segment. Each
rendition group is a single-track addendum, verified with `muxl verify` before
it joins the live window. Non-MUXL input ends the session.

The listener serves a broadcast on request and shares it among subscribers. A
stream must be live on the segment bus. New subscribers start at the newest
segment, source tracks take priority, and subscribers that fall behind skip
ahead. A broadcast ends with its stream session or when a track's codec
configuration changes; the next request gets a new catalog.

## Find the code

| Area                                       | Start here                                                                                                                                                |
| ------------------------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Publishing, subscribing and track assembly | [`pkg/replication/websocketrep/moq.go`](../pkg/replication/websocketrep/moq.go)                                                                           |
| Listener certificates                      | [`pkg/moq/cert.go`](../pkg/moq/cert.go)                                                                                                                   |
| Browser connection and player lifecycle    | [`moq-player.tsx`](../js/web/src/components/player/moq-player.tsx)                                                                                        |
| Browser discovery                          | [`moq-source.ts`](../js/web/src/components/player/moq-source.ts)                                                                                          |
| Replication tests                          | [`moq_test.go`](../pkg/replication/websocketrep/moq_test.go) and [`moq_peer_identity_test.go`](../pkg/replication/websocketrep/moq_peer_identity_test.go) |

## Measuring live latency

Use the configured [Docker builder](../.claude/skills/streamplace-docker/SKILL.md)
and the existing browser harness. Build a fresh frontend and node before running:

```sh
make app
make dev
E2E_LATENCY=1 hack/e2e-web-local.sh --project=chromium flows/live-latency.spec.ts
```

The harness creates a temporary account and isolated services. In latency mode,
the browser publishes a real 640 × 360, 30 fps H.264/Opus WHIP stream containing
frame identifiers and a continuous audio tone. It observes the DOM MoQ player
and a native WHEP receiver against the publisher's monotonic clock. Traces and
video recordings are disabled to keep the temporary stream key out of artifacts.

After ten seconds of settling, the test records twenty seconds of frame ages,
new-frame gaps, RTC counters and actual speaker PCM. It checks source health,
video progress, audible output and advancing native audio-worklet cursors.
`live-latency.json` in the Playwright output contains raw observations and
summaries. Repeated observations of the same canvas frame do not count as new
frames; callback counts are distinct from decoder throughput.

For a build comparison, select each frozen build with `E2E_BUILDDIR` and record
`E2E_REVISION` and `E2E_BINARY_SHA256`. The build directory must contain the
launcher, binary and compatible native libraries. Keep the measurement source
and libraries identical, alternate build order and run one experiment at a time.

Compare each transport with itself across builds. MoQ measures when the parent
observes its canvas on an animation frame; WHEP uses the video's expected
presentation time. Both measure generated-frame age, with different display
endpoints. Neither measures camera capture or physical screen latency. Report
steady playback separately from publish-to-first-frame time, and keep failed
health checks alongside the results.

## Testing multiresolution Auto

Inside the configured Docker builder, build fresh bundles and native code, then
generate the separate multiresolution fixtures and use the existing harness:

```sh
make app
make dev
bash test/ertmp/generate.sh --abr "$PWD/.build/ertmp-abr-fixtures"
E2E_ERTMP=1 E2E_ERTMP_FIXTURES_DIR="$PWD/.build/ertmp-abr-fixtures" \
  hack/e2e-web-local.sh --project=web moq-abr.spec.ts
```

The fixture publisher sends 90p and 180p H.264, 360p AV1 and AAC over one
Enhanced RTMP connection. The default fixtures and manual-quality/HLS test
remain available without `E2E_ERTMP_FIXTURES_DIR`.

The test serializes real encrypted QUIC datagrams through a bounded UDP proxy,
changing its downstream ceilings from 4 Mbps to 1 Mbps and back. Each packet
receives its full serialization time; delayed timers cannot create catch-up
bursts. The queue holds at most 200 ms of traffic. Browser ACKs and upstream
control traffic remain unthrottled. Recorded byte counts include encrypted QUIC
payloads and exclude IP/UDP headers. Timer granularity can underfill the ceiling,
so inspect the actual counters and packet service times.

Assertions cover changing canvas pictures, decoded dimensions and codecs,
steady low quality, recovery and PCM audio output. `ertmp-abr.json` records
limit changes, painted and decoded frames, subscription/decoder events, player
estimates and traffic counters. Phase screenshots accompany it. Selected
catalog configuration alone does not prove a successful handoff.

A diagnostic proxy with 3 KB of token-bucket burst credit produced quality
oscillation despite enforcing its average rate. On short RTTs, those bursts can
inflate BBR's maximum delivery samples. The serialized test establishes bounded
fixed-rate switching; bursty-link behavior and long-running stability need
separate evaluation. Report downshift and recovery times separately, including
the canvas sampling interval.

## Maintaining the browser SDK patch

`@moq/watch@0.6.6` applies the connection's bitrate estimate even when no
rendition advertises a bitrate, which changes automatic selection from the
best supported resolution to the first catalog entry. The registered
`patches/@moq__watch@0.6.6.patch` skips that filter when all rates are unknown.
Manual selection and stalled fallbacks keep their existing behavior. With
measured rates, the patch reserves audio and uses the existing resolution
preference after intersecting affordability and dimension filters. A zero video
budget remains a real budget. The handoff fixes use existing subscription
priorities and decoder identities, as described above.

The patch also waits for native `VideoDecoder` pending requests to drain before
pulling more media from the existing bounded consumer. The native `dequeue`
event and the SDK's effect cancellation handle pause, close and codec errors.
This caps pending coded requests at one, preserving accepted codec work and the
decoded presentation buffer. It does not bound end-to-end latency or discard
frames outside the consumer's existing age policy.

The readable source, regression tests and packaging configuration are in
`patches/moq-watch-0.6.6-source.patch`, against upstream commit
[`9da79786c2392271e51313f4fc24487f47d36975`](https://github.com/moq-dev/moq/commit/9da79786c2392271e51313f4fc24487f47d36975).
Use Bun 1.4.2, pinned by that commit's Nix development shell. The verified Linux
x64-baseline release archive has SHA-256
`c678040f14fe0440eb839d37cbd0ce4c051a32da72806ac97de6a6aab6bf728f`.

Inside the configured builder, with that Bun version on `PATH`:

```sh
moq_sdk_dir=.build/moq-watch-0.6.6
git clone --filter=blob:none --no-checkout https://github.com/moq-dev/moq.git "$moq_sdk_dir"
git -C "$moq_sdk_dir" fetch --depth=1 origin 9da79786c2392271e51313f4fc24487f47d36975
git -C "$moq_sdk_dir" sparse-checkout set js
git -C "$moq_sdk_dir" checkout --detach 9da79786c2392271e51313f4fc24487f47d36975
git -C "$moq_sdk_dir" apply "$PWD/patches/moq-watch-0.6.6-source.patch"
(
  cd "$moq_sdk_dir"
  bun install --frozen-lockfile --filter moq --filter @moq/watch
  cd js/watch
  bun run test ./src/media.test.ts ./src/player.test.ts ./src/video/source.test.ts ./src/video/decoder.test.ts
  bun run build
)
pnpm patch @moq/watch@0.6.6 --edit-dir .build/moq-watch-package
cp "$moq_sdk_dir/js/watch/dist/player-B6mw85kT.js" .build/moq-watch-package/
pnpm patch-commit .build/moq-watch-package
```

The unpatched normal build reproduced all 138 published package files byte for
byte. The patch build keeps the published shared-chunk filename and disables
source maps. Copy only that generated chunk: its runtime changes are rendition
selection, audio budgeting, handoff priorities, decoder reuse, video decoder
backpressure and removal of its source-map reference. Generated binding names
in adjacent regions can change with the build; review the readable source patch
and regenerate the runtime. The generated chunk's SHA-256 is
`ddefda27ac5ed6a133fb417e97304cbddb8fa6327a07be1811593db0754cc48b`.
`moq-quality.test.ts` checks the installed package so a missing patch fails the
ordinary web test suite.

The real MoQ playback test reproduced 89 pending decode requests, then passed
with at most one while video, PCM audio output, pause/resume and decoder cleanup
remained functional. Its existing receiver-thread pressure also exercises the
native path. The multitrack test verifies actual H264 and supported AV1 decoding
and switching. These checks establish queue behavior, not long-run latency
stability; source aging and shared host load require separate measurements.
