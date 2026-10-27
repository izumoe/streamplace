import { expect, test, type Page } from "@playwright/test";
import { loginThroughPds } from "./login";

test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => {
    localStorage.setItem("player-transport", "hls");
  });
});

// Mirror of .maestro/logged-out/stream.yaml: open the looping test stream from the home
// feed and confirm playback context loads. The "Now streaming - e2e test stream"
// chat system message arrives over the chat socket shortly after open.
test("stream: open test stream from feed", async ({ page }) => {
  await page.goto("/");

  const card = page
    .getByRole("link")
    .filter({ hasText: "e2e test stream" })
    .first();
  await expect(card).toBeVisible({ timeout: 30_000 });
  await card.click();

  // the stream page mounts a video element and posts a "Now streaming - ..."
  // system message into chat once playback context is established
  await expect(
    page
      .getByText("Now streaming - e2e test stream")
      .filter({ visible: true })
      .first(),
  ).toBeVisible({ timeout: 30_000 });
  await expect(page.locator("video").first()).toBeVisible();
});

test("stream: portrait player exposes playback controls", async ({ page }) => {
  await page.setViewportSize({ width: 320, height: 568 });
  await page.goto("/");

  const card = page
    .getByRole("link")
    .filter({ hasText: "e2e test stream" })
    .first();
  await expect(card).toBeVisible({ timeout: 30_000 });
  await card.click();

  // Chat may virtualize its initial system message once history fills up.
  // Playback controls depend on the player being ready, not that chat row.
  const video = page.locator("video").first();
  await expect(video).toBeVisible({ timeout: 30_000 });
  await expect
    .poll(() =>
      video.evaluate(
        (element: HTMLVideoElement) =>
          element.getVideoPlaybackQuality().totalVideoFrames,
      ),
    )
    .toBeGreaterThan(0);
  const player = video.locator("..");
  await player.hover();
  await player.locator('button[title="Unmute"]').click();
  const muteButton = player.locator('button[title="Mute"]');
  await expect(muteButton).toBeVisible();
  await expect
    .poll(() => video.evaluate((element: HTMLVideoElement) => element.muted))
    .toBe(false);

  // Unmuted playback hides idle controls, then pointer movement reveals them.
  const controlOpacity = () =>
    muteButton.evaluate((element) => {
      let opacity = 1;
      for (
        let parent: Element | null = element;
        parent;
        parent = parent.parentElement
      ) {
        opacity *= Number(getComputedStyle(parent).opacity);
      }
      return opacity;
    });
  await page.mouse.move(0, 0);
  await expect.poll(controlOpacity, { timeout: 5_000 }).toBeLessThan(0.01);
  await player.hover();
  await expect.poll(controlOpacity).toBeGreaterThan(0.99);
  await muteButton.click();
  await expect
    .poll(() => video.evaluate((element: HTMLVideoElement) => element.muted))
    .toBe(true);

  await player.getByRole("button", { name: "Fullscreen", exact: true }).click();
  await expect
    .poll(() => page.evaluate(() => Boolean(document.fullscreenElement)))
    .toBe(true);
  const exitFullscreen = page
    .locator(":fullscreen")
    .getByLabel("Exit fullscreen");
  await expect(exitFullscreen).toBeVisible();
  await exitFullscreen.click();
  await expect
    .poll(() => page.evaluate(() => Boolean(document.fullscreenElement)))
    .toBe(false);
  await expect(
    player.getByRole("button", { name: "Fullscreen", exact: true }),
  ).toBeVisible();
});

// Responsive layout changes must preserve the playing element and source.
// Restarting hls.js would assign a new blob URL to the video.
async function expectVideoSurvivesResize(page: Page) {
  const video = page.locator("video").first();
  await expect
    .poll(() =>
      video.evaluate((v: HTMLVideoElement) => !v.paused && v.readyState >= 2),
    )
    .toBe(true);
  await video.evaluate((v: HTMLVideoElement) => {
    const src = v.src + (v.querySelector("source")?.src ?? "");
    (window as any).__spVideo = { el: v, src, srcObject: v.srcObject };
  });

  const videoWidth = () =>
    page.evaluate(
      () => document.querySelector("video")?.getBoundingClientRect().width,
    );
  for (const width of [900, 1280]) {
    const oldWidth = await videoWidth();
    await page.setViewportSize({ width, height: 720 });
    // The layout has caught up once the video's box changes.
    await expect.poll(videoWidth).not.toBe(oldWidth);
    const state = await page.evaluate(() => {
      const before = (window as any).__spVideo;
      const v = document.querySelector("video")!;
      const src = v.src + (v.querySelector("source")?.src ?? "");
      return {
        sameElement: v === before.el,
        sameSource: src === before.src && v.srcObject === before.srcObject,
      };
    });
    expect(state).toEqual({ sameElement: true, sameSource: true });
    await expect(video).toBeVisible();
    const before = await video.evaluate((v: HTMLVideoElement) => v.currentTime);
    await expect
      .poll(() => video.evaluate((v: HTMLVideoElement) => v.currentTime))
      .toBeGreaterThan(before);
  }
}

test("stream: resizing the window keeps the same playing video", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1280, height: 720 });
  await page.goto("/");
  const card = page
    .getByRole("link")
    .filter({ hasText: "e2e test stream" })
    .first();
  await expect(card).toBeVisible({ timeout: 30_000 });
  await card.click();
  await expectVideoSurvivesResize(page);
});

// Signing in must not make responsive layout changes restart HLS playback.
test("stream: the streamer's own HLS video survives a resize", async ({
  page,
}) => {
  const httpsUrl = process.env.SERVER_HTTPS_URL;
  test.skip(!httpsUrl, "harness started without its HTTPS hostnames");
  await page.setViewportSize({ width: 1280, height: 720 });
  await loginThroughPds(page);

  await page.goto(`${httpsUrl}/`);
  const playlist = page.waitForResponse((response) => {
    const url = new URL(response.url());
    return (
      url.pathname === "/xrpc/place.stream.playback.getLivePlaylist" &&
      url.searchParams.has("streamer") &&
      !url.searchParams.has("track")
    );
  });
  await page
    .getByRole("link")
    .filter({ hasText: "e2e test stream" })
    .first()
    .click();
  expect((await playlist).ok()).toBe(true);
  await expectVideoSurvivesResize(page);
});
