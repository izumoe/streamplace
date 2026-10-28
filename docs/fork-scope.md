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
the existing operator stream allowlist. Authentication and bans still apply;
recording also requires the user's existing recording setting. Old database
tables are left intact, with no destructive migration.

## Useful core

Keep the HTTP/XRPC server, DOM frontend, identity resolution and OAuth, signing
and verification, moderation, live discovery, ingest supervision, live media bus
and MoQ distribution together for now. VOD, notifications and native update
hosting remain bundled in this pass.

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
authorization mechanism. Future splits should reuse task callbacks and blob
store boundaries when a deployment actually needs them.

The Docker workflow supports isolated sibling checkouts with unique basenames.
Use that layout: equal worktree basenames can collide on container names, and a
Git common directory outside the mounted parent is unavailable in the container.
