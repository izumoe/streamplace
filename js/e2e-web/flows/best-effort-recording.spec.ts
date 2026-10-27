import { expect, test } from "@playwright/test";

// The harness runs the node with debug recording ON for the test account and a
// deliberately broken recording sink (see `streamplace e2e`), so every ingest
// session attempts a recording that fails. Debug recording is best-effort: a
// failed upload (billing, credentials, an unwritable disk) must never stall the
// ingest pipeline. Regression: a dead recorder used to wedge it, leaving a node
// that could not stream at all while recording was enabled.
test("best-effort-recording: a failing recording sink does not block playback", async ({
  page,
}) => {
  const did = process.env.ACCOUNT_DID;
  if (!did) {
    throw new Error("ACCOUNT_DID is not set — start the harness first");
  }

  await page.addInitScript(() => {
    localStorage.setItem("player-transport", "hls");
  });
  await page.goto("/");

  const card = page
    .getByRole("link")
    .filter({ hasText: "e2e test stream" })
    .first();
  await expect(card).toBeVisible({ timeout: 30_000 });
  await card.click();

  const video = page.locator("video").first();
  await expect(video).toBeVisible({ timeout: 30_000 });
  const frames = () =>
    video.evaluate(
      (element: HTMLVideoElement) =>
        element.getVideoPlaybackQuality().totalVideoFrames,
    );
  await expect.poll(frames).toBeGreaterThan(0);
  const initialFrames = await frames();

  // And the stream stays up. The harness restarts its WHIP stream every few
  // seconds, so checking well past that window means recording failed again and
  // again without the broadcast going dark. The wait also clears the node's
  // 30s live-window retention: the playlist query is served from that window, so
  // only a stream still ingesting fresh segments answers 200 after it (a stream
  // that stopped would keep serving stale segments for up to 30s).
  await page.waitForTimeout(35_000);
  await expect
    .poll(
      async () => {
        const res = await page.request.get(
          `/xrpc/place.stream.playback.getLivePlaylist?streamer=${did}`,
        );
        return `${res.status()}`;
      },
      {
        timeout: 30_000,
        message: "stream still live after repeated recording failures",
      },
    )
    .toBe("200");
  await expect.poll(frames).toBeGreaterThan(initialFrames);
});
