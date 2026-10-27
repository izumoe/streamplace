import { expect, test } from "@playwright/test";

// Firefox's native "Block Audio and Video" policy also blocks muted autoplay.
test.use({
  browserName: "firefox",
  launchOptions: {
    firefoxUserPrefs: {
      "media.autoplay.default": 5,
      // Playwright reads grant sticky activation. Require actual input at play().
      "media.autoplay.blocking_policy": 2,
    },
  },
});

// The play button belongs to the video's autoplay attempt, which a layout
// change doesn't repeat: it has to stay up until the viewer presses it.
test("autoplay-blocked: the play button survives a resize", async ({
  page,
}) => {
  await page.addInitScript(() => {
    localStorage.setItem("player-transport", "hls");
  });
  await page.setViewportSize({ width: 900, height: 720 });
  await page.goto(`/${process.env.ACCOUNT_DID}`);
  const playButton = page
    .getByRole("button", { name: "Play", exact: true })
    .filter({ hasText: "Play" });
  await expect(playButton).toBeVisible();

  // Through the desktop layout and back, which moves the player.
  const video = page.locator("video").first();
  await expect
    .poll(() =>
      video.evaluate((element: HTMLVideoElement) => element.readyState),
    )
    .toBeGreaterThanOrEqual(2);
  expect(
    await video.evaluate((element: HTMLVideoElement) => element.paused),
  ).toBe(true);
  const original = await video.elementHandle();
  const originalSource = await video.getAttribute("src");
  const originalTime = await video.evaluate(
    (element: HTMLVideoElement) => element.currentTime,
  );
  const videoWidth = () =>
    video.evaluate((v: HTMLVideoElement) => v.getBoundingClientRect().width);
  for (const width of [1280, 900]) {
    const oldWidth = await videoWidth();
    await page.setViewportSize({ width, height: 720 });
    await expect.poll(videoWidth).not.toBe(oldWidth);
    expect(
      await video.evaluate((element, before) => element === before, original),
    ).toBe(true);
    await expect(video).toHaveAttribute("src", originalSource!);
    expect(
      await video.evaluate((element: HTMLVideoElement) => element.paused),
    ).toBe(true);
    expect(
      await video.evaluate((element: HTMLVideoElement) => element.currentTime),
    ).toBe(originalTime);
  }
  await expect(playButton).toBeVisible();

  await playButton.click();
  await expect
    .poll(() => video.evaluate((v: HTMLVideoElement) => !v.paused))
    .toBe(true);
  await expect(playButton).toBeHidden();
  const frames = () =>
    video.evaluate(
      (element: HTMLVideoElement) =>
        element.getVideoPlaybackQuality().totalVideoFrames,
    );
  const before = await frames();
  await expect.poll(frames).toBeGreaterThan(before);
  await expect
    .poll(() =>
      video.evaluate((element: HTMLVideoElement) => element.currentTime),
    )
    .toBeGreaterThan(originalTime);
});
