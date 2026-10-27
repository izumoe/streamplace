# Fork scope and later service boundaries

The first pass uses the existing DOM web app, adds browser MoQ playback and
keeps the native mobile apps and optional services bundled. It removes React
Native Web hosting, browser exports, frontend selection and its beta cookie.
It does not promise feature parity with the retired browser interface.

This is a source and startup audit, not usage research. Technical coupling is
visible in the repository; demand for these features is unknown. The candidates
below need a deployment or product decision before removal. No binary size,
build-time or runtime improvement has been measured for these proposed splits.

## Useful core

Keep the HTTP/XRPC server, DOM frontend, identity resolution and OAuth, signing
and verification, moderation, live discovery, ingest supervision, the live media
bus and MoQ distribution together for now. WebRTC and HLS remain playback
fallbacks. Basic storage and the existing ingest worker process are already
useful boundaries. A new general media RPC layer would add work without an
established consumer.

## Ranked follow-ups

| Candidate                                    | Existing boundary and expected benefit                                                                                                                                                                                                                                                                               | Cost or behavior lost                                                                                                                                                                                                                                                                                                                                                                |
| -------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Embedded Livepeer gateway                    | `--livepeer-gateway-url` already supports an external gateway. `pkg/cmd/go_livepeer.go` starts the embedded gateway only when selected. Moving the embedded runner out could reduce the core's build graph and flag surface. High confidence in the existing boundary.                                               | `pkg/config/config.go`, `pkg/config/livepeer_flags.go` and `pkg/cmd/streamplace.go` import Livepeer's starter, so removing its startup call alone does not remove that dependency. Keep remote transcoding and local media processing working; local GStreamer work is independent.                                                                                                  |
| Iroh distribution                            | `pkg/cmd/streamplace.go` creates `iroh_replicator.NewSwarm` only when configured. `hack/build-native.sh` builds Iroh and MoQ native artifacts together. A MoQ-focused deployment could later drop the unused transport's native build. High confidence in build coupling; replacement completeness needs validation. | `pkg/replication/iroh_replicator/kv.go` is active: it distributes signed segments, origins and viewer counts. MoQ video transfer alone does not replace all those responsibilities. The empty, unreferenced `IrohReplicator` in `iroh.go` is a separate dead stub, not evidence that the swarm is unused.                                                                            |
| View-count aggregation and CDN log ingestion | `pkg/viewlog`, `pkg/cdn/bunny` and the callbacks wired in `pkg/cmd/streamplace.go` offer a later worker boundary. Keeping slow aggregation away from interactive work could improve isolation. High confidence in the task boundary.                                                                                 | `pkg/statedb/queue_processor.go` currently puts aggregation, CDN ingestion, notifications and chat on the general queue. A split must assign task ownership and provide state, keys and blob access. Moving aggregation alone leaves request logging in `pkg/spxrpc/viewlog_hooks.go`. These jobs are conditional on configuration and a VOD store; live viewer counts are separate. |
| Desktop packaging and update delivery        | `js/desktop` already packages a separate application that launches the node. `pkg/api/desktop-updates.go` serves its update mechanism. Omitting desktop releases is a relatively low-coupling way to reduce release and signing work. High confidence in packaging separation.                                       | Users lose the desktop installer and updater. OBS ingest uses standard RTMP/WHIP; it is not an embedded OBS application and should remain. Existing chat/embed routes also serve OBS browser sources.                                                                                                                                                                                |
| Native mobile artifact hosting               | Native OTA exports are embedded through `js/app`, with manifest assets served by `pkg/api` and update metadata built by the existing updater. A later artifact deployment could remove mobile export work from node builds. Medium confidence in extraction cost.                                                    | Branding defaults, fonts and exact manifest-listed asset paths still depend on those assets. Native Expo apps and OTA remain supported in this pass. Do not remove their exports merely because browser exports are gone.                                                                                                                                                            |
| Beta invite/request policy                   | VOD invites arrived in `9ba627404` (2026-05-15), requests in `9801086be` (2026-05-30). An operator choosing a plain allowlist could simplify rollout policy. Existing configuration already supports operating without an invite issuer. High confidence in the policy coupling.                                     | Invites gate uploads and durable livestream recording in `pkg/director/s3_upload.go`; indexing, notifications and UI status also consume them. Removing the rollout UI must not silently open upload or recording authorization.                                                                                                                                                     |

View logging and aggregation were added in `9057bee00` and `e24f407a2`
(2026-05-17); Bunny provider support followed in `32193d985` (2026-09-06).
Those dates put them within the requested six-month review window. Age alone
does not establish that they should be removed.

## Keep unless the deployment changes

- VOD, recording and clips have active API, queue, signing and publication paths.
  Removing them loses durable media and requires more work than deleting a UI.
- Moderation, trusted verifications and label synchronization affect acceptance
  of media and accounts. They belong in the trust boundary of this fork.
- Production uses external PDS services. The full local PDS/PLC belongs to the
  development and end-to-end harness; it is not a production service embedded
  in the node.
- Automatic certificates (`dbbd3c363`, 2026-09-02) support secure listeners.
  File-based TLS is already available. An HTTPS reverse proxy alone does not
  supply the QUIC listener's TLS certificate.
- S3/local blob storage and the static/Bunny CDN provider boundary already
  exist. Removing S3 loses shared durable storage, not just a provider flag.
- `js/playback-router` is already an external Cloudflare worker. Its HLS health
  checks and `{servers}` response need deployment-specific review if it will
  participate in MoQ discovery. Lack of a Go import does not establish disuse.

## Smaller and simpler

The immediate simplification is one browser frontend, one browser build and no
runtime selector. Native OTA routes reuse the updater's MIME map rather than a
second asset inventory. Browser tests use the real same-origin node and the
existing login helper instead of saved node overrides or a parallel launcher.

Later extraction should reuse the external Livepeer URL, existing ingest worker,
task callbacks and blob store boundaries. Keep services bundled until a concrete
deployment needs a split; avoid adding speculative flags or a service framework.

The documented Docker workflow already supports isolated sibling checkouts with
unique basenames. Use that supported layout: managed worktrees with the same
basename can collide on container names, and a Git common directory outside the
mounted parent is unavailable inside the container. A unique sibling checkout
avoids both without a workaround script or broader mount.
