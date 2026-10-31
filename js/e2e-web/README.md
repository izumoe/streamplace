# e2e-web (Playwright)

Browser regression flows for the DOM web app. Playwright uses the same isolated
node/PDS harness as the mobile `.maestro/` suite, with real media and APIs.

## How it fits together

- **Harness:** `streamplace e2e` (see `pkg/cmd/e2e.go`) boots a node + local
  PDS/PLC + a looping WHIP test stream and prints `SERVER_URL` /
  `ACCOUNT_HANDLE` / `ACCOUNT_DID` (and `E2E_FIXTURE_MP4`, the local file it
  streams). The node also serves the web app (embedded
  via `//go:embed all:dist/**`), so Playwright just points a browser at
  `SERVER_URL`.
- **Browser app:** the app uses its own origin as the node. Global setup waits
  for `getLiveUsers` and verifies that the real home stream card appears. No
  saved node override or shared browser storage is needed.
- **Projects:** the normal config runs `flows/` plus the browser cutover and
  MoQ playback checks in `../web/e2e/`. Firefox also runs the browser specific
  wheel-scroll regression. The other web specs use `js/web/playwright.config.ts`.

## Adding scenarios

Name specs `flows/<feature-or-behavior-slug>.spec.ts`, without sequence numbers.
Playwright automatically discovers specs under `flows/`; adding a scenario
does not require editing the config or a manifest.

Each test must establish its own prerequisites: navigate to the page it needs,
log in through `loginThroughPds` when authentication is required, and wait for
the relevant stream or application state. Tests get isolated browser contexts.
Never rely on prior test state or filename execution order. The suite runs
serially to limit contention on its shared harness account and looping stream,
not to establish dependencies between scenarios.

## Mobile coverage convention

For shared behavior, use the corresponding semantic slug and mention its mobile
path in the spec comment; no central parity manifest is needed. Logged-out
smoke, navigation, and stream coverage correspond to
`.maestro/logged-out/{smoke,tabs,stream}.yaml`. OAuth setup corresponds to
`.maestro/setup/oauth-login.yaml`; authenticated chat/profile behavior is covered
on mobile by `.maestro/logged-in/{chat-reply,chat-profile}.yaml`.

`invalid-handle` moves the harness account to a real unverifiable `.test`
handle and calls `com.atproto.identity.refreshIdentity`. The feed must show its
DID without `@`, open its DID-addressed stream, play media and copy a usable
link. The actual node search excludes invalid handles. Cleanup restores the
verified handle and waits for the live-feed cache, including after failure.
The corresponding native flow is `.maestro/logged-out/invalid-handle.yaml`.
The DOM login modal still supports typeahead, covered by the shared core hook
checks; the full `/login` page uses its own simple handle form.

`tabs` drives Home and Settings through the DOM navigation. `go-live` checks
that `/dashboard` requires login. Chat popout checks that the native link opens
one working chat window while preserving its stream tab. `chat-wheel-scroll`
checks small wheel deltas in both directions in the native scroll container,
including Firefox. Text selection and long content remain separate chat checks.
`vod` checks the node's page title and the hydrated app title.

`branding-favicons` uploads the generic favicon through the actual editor and
sets color-scheme variants through the authenticated API. It checks applicable
HTML icon links, scheme changes, persistence and deletion fallbacks, and decodes
`/favicon.ico` as a browser image to verify MIME, signature and pixels. The DOM
editor does not expose the retired browser app's variant controls or previews.

`stream` covers opening the live feed and the narrow portrait layout at
320 × 568. It exercises mute, idle control hiding/revealing and fullscreen.
Portrait controls wait for decoded frames. Responsive checks preserve the same
playing video and source both anonymously and after real OAuth login.

`player-viewport` starts the real harness's HLS stream at phone, tablet, square, and desktop sizes. After confirming playback,
it pauses through the normal shortcut and checks control visibility, viewport
containment, opacity, and pointer hit targets with chat open and closed. It
changes volume in both states, opens Settings, and checks fullscreen entry/exit
and control availability in fullscreen. Cases straddle the square-layout and
player-width boundaries; compact players retain theatre and danmu in Settings.
The touch case uses the embed player to verify 44px buttons and a working volume
hit area at narrow and tablet widths. Identity and playback use the real local
services. This is web-only: iOS and Android use the separate Expo/native player.

`player-breakpoints` keeps one HLS video playing across nine viewport sizes:
407/408/409 × 844, 799/800/801 × 800, and 1023/1024/1025 × 720. These cross the
compact-control, square-layout and desktop boundaries. Every size must retain
the same video element and source, continue decoding frames, and expose working
mute controls inside the viewport. Players at least 408px wide also expose a
working volume slider. Native live playback uses device volume controls.

`auto-publish-vods` saves and reloads the automatic publishing preference in
Privacy & Security. It delays a real recording opt-out PUT to check that another
toggle cannot overwrite consent while saving. The retired browser stream form's
Create tab is not part of the DOM app. PDS index deletion and publication behavior
remain covered by `pkg/atproto/auto_publish_vod_test.go` and the statedb tests.

`syndication` plays the test stream from the harness's second node, which pulls
it from the first over Media over QUIC (`SERVER2_URL`; see `docs/moq-for-devs.md`). It is
web-only: node-to-node transfer has no mobile surface, and the mobile flows play
from the ingest node.

## OAuth over real HTTPS

`oauth-login` logs in the way a user does, through the node's OAuth proxy
and the local PDS's sign-in and consent pages. atproto OAuth will not run over
plain HTTP, with ports, or on `.test`-style names, and parts of it resolve
`did:plc` against a hardcoded `https://plc.directory`. So
`hack/e2e-web-local.sh` starts the harness with public DNS names for 127.0.0.1:
`--https-pds-hostname localhost-pds.streamplace.network` (plus a wildcard record
under it, for account handles) and
`--https-station-hostname localhost-station.streamplace.team` (the node's
broadcaster host: the app and the OAuth client). The two must be on different
registrable domains, as in production — the PDS rejects a same-site navigation
to its sign-in page. In that mode the harness:

- mints a throwaway CA and serves both on 127.0.0.1:443, routed by SNI (so it
  must be able to bind 443 — the container runs as root);
- publishes this build's lexicons into a local lexicon authority account (the
  stand-in for the account behind `_lexicon.stream.place`) and has the PDS
  resolve lexicons from it, so the OAuth permission set the app asks for
  (`include:place.stream.authFull`) is this branch's, not production's;
- runs a small proxy that sends `plc.directory` to the local PLC; the node and
  the browser use it;
- has each process trust the CA on its own (Go `SSL_CERT_DIR`, Node
  `NODE_EXTRA_CA_CERTS`, Chromium the leaf's SPKI) — nothing is installed
  system-wide;
- additionally prints `SERVER_HTTPS_URL`, `PDS_HTTPS_URL`, `E2E_PROXY_URL` and
  `E2E_TLS_SPKI`, which the config and the flow pick up.

Unauthenticated flows keep using the plain-HTTP `SERVER_URL`; OAuth and chat
popout tests establish their own login using the HTTPS URLs. Run with
`E2E_HTTPS_PDS_HOSTNAME=` to skip HTTPS; those tests then skip themselves.
Details are in `pkg/cmd/e2e_https.go`.

## Run it locally

```bash
# once: install the browser
pnpm --filter @streamplace/e2e-web install-browser

# build a streamplace binary that embeds the web app, then run the suite
make dev
hack/e2e-web-local.sh
```

`hack/e2e-web-local.sh` starts the harness, waits for `SERVER_URL`, runs the
flows, and tears the harness down. On a host without the cgo runtime, run it
inside the build container.

Artifacts on failure (traces, screenshots, video) land in `test-results/` and a
report in `playwright-report/` (`pnpm --filter @streamplace/e2e-web report`).
