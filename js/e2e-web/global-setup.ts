import { chromium, expect, type FullConfig } from "@playwright/test";

// The browser app uses its own origin as the node. Wait for actual ingest
// readiness before any flow loads the home feed.
export default async function globalSetup(_config: FullConfig) {
  const SERVER_URL = process.env.SERVER_URL;
  if (!SERVER_URL) {
    throw new Error("SERVER_URL is not set — start the harness first");
  }
  // The latency flow supplies its own marked source after setup.
  if (process.env.E2E_LATENCY === "1") return;

  const browser = await chromium.launch();
  const page = await browser.newPage();
  try {
    // The harness WHIP stream takes a few seconds after boot to ingest its
    // first segments and register as live. The home feed fetches getLiveUsers
    // on mount and doesn't aggressively re-poll, so wait for the stream to be
    // live *before* loading the feed — otherwise the flows race the stream and
    // see an empty "no one is streaming" feed.
    await waitForLiveStream(SERVER_URL);

    await page.goto(`${SERVER_URL}/`);
    await expect(
      page.getByRole("link").filter({ hasText: "e2e test stream" }).first(),
    ).toBeVisible({ timeout: 30_000 });
  } finally {
    await browser.close();
  }
}

// Poll getLiveUsers until the harness's looping test stream shows up (it needs
// a few seconds after boot to ingest its first segments).
async function waitForLiveStream(serverUrl: string, timeoutMs = 90_000) {
  const deadline = Date.now() + timeoutMs;
  let lastErr = "";
  while (Date.now() < deadline) {
    try {
      const res = await fetch(
        `${serverUrl}/xrpc/place.stream.live.getLiveUsers?limit=50`,
      );
      if (res.ok) {
        const body = (await res.json()) as { streams?: unknown[] };
        if (Array.isArray(body.streams) && body.streams.length > 0) return;
      } else {
        lastErr = `status ${res.status}`;
      }
    } catch (e) {
      lastErr = (e as Error).message;
    }
    await new Promise((r) => setTimeout(r, 2000));
  }
  throw new Error(
    `timed out waiting for a live stream from ${serverUrl} (last: ${lastErr || "empty feed"})`,
  );
}
