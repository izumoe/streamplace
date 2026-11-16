# Fork scope and later service boundaries

The fork uses the DOM web app with browser MoQ playback. WebRTC and HLS remain
fallbacks. React Native Web hosting, browser exports, frontend selection and its
beta cookie are removed. The DOM app's feature set is the browser baseline;
native mobile apps and their OTA exports remain supported.

Live renditions use a separate [transcoder service](transcoder.md). The node no
longer embeds Livepeer, imports its packages or exposes its flags. Signing,
verification and ordered publication stay in the node. VOD processing keeps its
existing implementation.

Desktop installers, Electron packaging, updater APIs and release jobs are
removed. Node binaries, mobile downloads and OBS browser-source routes remain.
Retired updater and desktop download routes return 404.

Beta invites and requests are removed from the API, lexicons, indexing,
notifications, database models and interfaces. Uploads and durable recording use
an independent VOD capability, bootstrapped from the existing operator stream
allowlist. Local publishing approval and node chat restrictions use
[node access](node-access.md). Authentication and bans still apply; recording
also honors the user's existing recording setting. Old database tables are left
intact, with no destructive migration.

## Useful core

Keep the HTTP/XRPC server, DOM frontend, identity resolution and OAuth, signing
and verification, moderation, live discovery, ingest supervision, live media bus
and MoQ distribution together for now. VOD, notifications and native update
hosting remain bundled in this pass.

Live latency work retains verified early source playback and adds connection-time
WebRTC subscription, persistent track pacing and a single 400 ms WHIP keyframe
requester. Muxl initializes before readiness; stalled worker socket writes use
the existing reconnect buffer after a five-second deadline. HLS keeps each
stream's writer and publication status together, with distinct writers across
preview/public transitions. The browser retains live audio ordering and adds a
200 ms scheduling reserve through its existing MoQ sync mechanism. See
[measurement and limits](moq-for-devs.md#measuring-live-latency).

Native RTMP ingest accepts one to six H.264 or AV1 video tracks and exactly one
AAC audio track. It sends encoded packets directly to the shared signer,
preserving each source track and its identity without an internal RTMP relay.
The bitrate limit still applies to the aggregate stream. HEVC is unsupported;
AV1 codec or resolution changes require reconnecting. The optional legacy
MistServer comparison relay remains H.264-only. AV1 playback uses the original
video over MoQ; H.264 compatibility output for legacy transports requires the
configured transcoder service.

This is a source and startup audit, not usage research. Technical coupling is
visible in the repository; demand for optional features is unknown. No binary
size or build-time improvement has been measured for the candidates below.

## Later candidates

| Candidate                                    | Existing boundary                                                                         | Work needed before extraction                                                                                                                                                                |
| -------------------------------------------- | ----------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Iroh distribution                            | The swarm starts only when configured; native builds currently include both Iroh and MoQ. | Iroh distributes signed segments, origins and viewer counts. MoQ video transfer alone does not replace all of these responsibilities. Validate discovery and replication before removing it. |
| View-count aggregation and CDN log ingestion | `pkg/viewlog`, `pkg/cdn/bunny` and startup callbacks offer a worker boundary.             | The general queue also owns notifications and chat. Assign task ownership and provide state, keys and blob access. Request logging and live viewer counts remain separate.                   |
| Native mobile artifact hosting               | Expo manifests and their exact asset paths are already served through the updater.        | A separate artifact deployment could remove mobile exports from node builds. Preserve fonts, branding defaults and manifest-listed assets when moving it.                                    |

## Keep unless the deployment changes

- VOD, recording and clips have active API, queue, signing and publication paths.
- Moderation, trusted verifications and label synchronization affect acceptance
  of media and accounts.
- Production uses external PDS services. The local PDS/PLC belongs to the
  development and end-to-end harness.
- Automatic certificates support secure listeners. An HTTPS reverse proxy alone
  does not supply the QUIC listener's TLS certificate.
- S3/local blob storage and the static/Bunny CDN provider boundary already exist.
- `js/playback-router` is an external Cloudflare worker. Review its HLS health
  checks and server discovery for the target MoQ deployment.

## Smaller and simpler

The fork removes implementations and their dependency graphs rather than
keeping disabled startup paths. Shared GStreamer I/O and bus handling serve both
the node and the finite encoder job; there is no second native wrapper layer.
The existing stream allowlist replaces beta policy without adding another
authorization mechanism. The latency changes reuse the shared Muxl engine,
worker reconnect buffer, track timers and MoQ sync signal. They replace three
parallel HLS maps with one stream state and remove the duplicate PLI sender.
Future splits should reuse task callbacks and blob store boundaries when a
deployment actually needs them.

The Docker workflow supports isolated sibling checkouts with unique basenames.
Use that layout: equal worktree basenames can collide on container names, and a
Git common directory outside the mounted parent is unavailable in the container.

## Upstream catch-up, November 2026

Comparison: fork `885310dce` against upstream `next` at `a9dbc4d87`.
These branches have diverged; replacing whole files would lose the DOM player,
access policy, bios, chat ordering, signing and continuous-audio fixes.

The first port adopts upstream's transcoder authorization and explicit-empty
configuration semantics, the indefinite-archival expiration fix and five-minute
Web Push expiry. Additional signed-fixture regressions cover primary `did:key`
identity, audio-tail authorization and trusted cross-stream rendition replay.
The publication code retains its existing source-admission and reconnect checks.
See [verification and operator configuration](moq-for-devs.md#what-gets-verified).

Further candidates need individual ports:

- Audio stems: port receiver and viewer support for upstream streams without
  requiring per-stem ingest or the OBS plugin. Our eRTMP ingest goal is a main
  live mix and a separate VOD mix, with explicit live/recording routing.
- MoQ push and node roles: recording/transcoding leases coordinate through a
  shared database. They do not coordinate independent peer databases.
- Persistent web assets and update recovery: adapt the bounded cache and failure
  tests to Vite's existing worker and asset paths, preserving API/media exclusions.
- Metadata defaults/loading, indexed profile enrichment and pending chat removals
  are useful smaller ports. Preserve our access gates and ordered chat refresh.
- WHIP AV1 and upstream native/mobile dependency upgrades need separate validation;
  do not replace the optimized RTMP/muxl pipeline or change OTA compatibility as
  a side effect of a package version bump.

OBS/Desktop integration, Windows, GitLab/Tangled mirroring, dual frontend hosting
and beta invites remain excluded. Upstream `next` currently declares package
version 0.13.1; its separate v0.13.4 release commit is not an ancestor of `next`.
Version alignment must identify the fork's actual commit and must not imply that
all upstream changes or native SDK upgrades have been adopted.

### Received audio stems

The viewer port includes MoQ and WebRTC. Playback starts with the full mix;
custom mixing silences that mix before playing additive stems. Main/VOD are
alternative complete mixes and must never be summed or exposed as ordinary
stems.

Use optional catalog descriptors and audio-only child MoQ broadcasts. This
keeps the old main-mix catalog intact and reuses native CMAF validation and
codec descriptions. Publish child audio from the same validated source events;
do not add another encoder or re-sign received assets. All selected audio uses
one AudioContext and the video's existing synchronization clock.

- [x] Preserve received groups/labels in muxl and in completed main audio/EOS tails.
- [x] Make main-track selection, HLS, replication and WebRTC pairing group-aware.
- [x] Publish bounded optional MoQ stem broadcasts without rewriting PDS origins.
- [x] Add synchronized MoQ and WebRTC mixing with shared mute/volume/pause handling.
- [x] Add compact Vite controls and exercise switching, fallback and source changes.
- [x] Verify signed grouped input over real replication and both viewer transports.

Keep the fork's sample pacers, source-ingredient checks and bounded caches.
Missing stem codecs do not trigger per-stem transcoding. HLS stays on the main
mix. Stream changes reset mix controls; labels never grant permissions. The
separate eRTMP main/VOD ingest phase needs explicit live/recording routing.

The lowest audio group is the full mix; absent group metadata means group zero.
Up to two higher groups with Opus are offered as additive stems. Codecs within
one group are alternatives. Unsupported MoQ channel layouts or sample rates keep
the full mix rather than dropping channels. Stem decoders start only when the
viewer requests a custom mix; ordinary playback retains one audio output.

A receiving node with live public stem media advertises its own playback endpoint
using its existing listener/relay configuration. This avoids depending on the
origin to understand the optional child broadcasts. No origin record is rewritten.
The HLS window can retain this routing choice for up to 30 seconds after the last
stem segment; the live catalog removes obsolete controls immediately.

MoQ switches only at the shared audio cursor. A missing audible stem restores the
full mix when samples cover that cursor. If reconnecting main audio starts later,
playback waits rather than skipping audio independently of video, and the existing
10-second transport watchdog remains the recovery path. This is not a guarantee
of gap-free playback under arbitrary packet loss.

Verification uses real signed AVC/AAC/Opus input, independent origin and receiver
nodes, native MoQ replication and the shipping Chromium UI. Full mix, voice-only
control, restoration, video progress and audio cleanup pass over MoQ and WebRTC;
all three AVC WebRTC rungs retain three progressing audio streams. SDK worklet
tests cover shared-memory and postMessage rings, incompatible layouts, buffering
and synchronized switching. These checks do not replace listening on physical
devices or measure full-pipeline performance.

Input-local MoQ discontinuities preserve the shared cursor. Child and inactive
main decoders cannot move the video's reference; audible main audio retains
clock ownership. Content resets replace the renderer and rings together while
keeping the context warm. WebRTC full-mix playback stays native until the viewer
requests custom mixing.

WebRTC timing comes from the original signed sample timestamps. Independent
senders preserve offsets, missing intervals and spans that cross source batch
boundaries. At most four senders each queue four sample-slice references, without
copying payloads. Queued main media remains in catch-up accounting. A sender that
cannot accept work before its required deadline cancels playback explicitly
instead of dropping audio or retaining an unbounded queue.

Timing regressions cover positive and negative signed offsets across consecutive
batches, missing stems, audio-only tails, source idle, queued backlog and queue
cancellation. MoQ tests cover child and inactive-main clock isolation, both PCM
buffer transports, future heads and active-main/content resets.
