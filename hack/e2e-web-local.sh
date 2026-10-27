#!/usr/bin/env bash
#
# Run the headless-web e2e suite (Playwright) against a self-contained
# `streamplace e2e` harness — the web counterpart to hack/e2e-local.sh.
#
#   hack/e2e-web-local.sh [-- <extra playwright args>]
#
# Prerequisites:
#   * A streamplace binary with the embedded web bundle (`make dev`).
#     The web app uses its own origin as the node. Global setup waits for
#     the harness's real live stream before the browser flows start.
#   * Playwright + its chromium browser installed in js/e2e-web
#     (pnpm --filter @streamplace/e2e-web install-browser; needs root for
#     --with-deps, so run it inside the container).
#   * For the OAuth flow, the harness's HTTPS mode: see hack/lib/e2e-harness.sh.
#
# On a dev host without the cgo runtime, run this inside the build container.
set -euo pipefail

cd "$(dirname "$0")/.."
REPO="$PWD"

# shellcheck source=hack/lib/e2e-harness.sh
. hack/lib/e2e-harness.sh
e2e_harness_start

# --- run the flows ---------------------------------------------------------
# No `exec` here: that would replace this shell and discard the EXIT trap,
# leaving the harness and its children running after a successful run.
cd "$REPO/js/e2e-web"
pnpm exec playwright test "$@"
