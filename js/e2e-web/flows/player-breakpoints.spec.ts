import { expect, test } from "@playwright/test";

// Keep one playing element while crossing the compact controls and square
// viewport boundaries. The viewport suite covers each layout's full chrome.
test("player-breakpoints: volume works through responsive layout changes", async ({
  page,
}) => {
  test.setTimeout(180_000);
  await page.setViewportSize({ width: 1080, height: 722 });
  await page.addInitScript(() => {
    localStorage.setItem("player-transport", "hls");
  });
  await page.goto("/");
  await page
    .getByRole("link")
    .filter({ hasText: "e2e test stream" })
    .first()
    .click();
  const video = page.locator("video").first();
  const player = video.locator("..");
  const frames = () =>
    video.evaluate(
      (element: HTMLVideoElement) =>
        element.getVideoPlaybackQuality().totalVideoFrames,
    );
  await expect.poll(frames).toBeGreaterThan(0);
  const original = await video.elementHandle();
  const originalSource = await video.getAttribute("src");
  await player.hover();
  await player.locator('button[title="Unmute"]').click();

  for (const size of [
    { width: 407, height: 844 },
    { width: 408, height: 844 },
    { width: 409, height: 844 },
    { width: 799, height: 800 },
    { width: 800, height: 800 },
    { width: 801, height: 800 },
    { width: 1023, height: 720 },
    { width: 1024, height: 720 },
    { width: 1025, height: 720 },
  ]) {
    await test.step(`${size.width}x${size.height}`, async () => {
      await page.setViewportSize(size);
      await expect(video).toBeVisible();
      expect(
        await video.evaluate((element, before) => element === before, original),
      ).toBe(true);
      await expect(video).toHaveAttribute("src", originalSource!);
      const previousFrames = await frames();
      await expect.poll(frames).toBeGreaterThan(previousFrames);

      await player.hover();
      const mute = player.locator('button[title="Mute"]');
      await expect(mute).toBeInViewport({ ratio: 1 });
      await mute.click();
      await expect
        .poll(() =>
          video.evaluate((element: HTMLVideoElement) => element.muted),
        )
        .toBe(true);
      const unmute = player.locator('button[title="Unmute"]');
      await expect(unmute).toBeInViewport({ ratio: 1 });
      await unmute.click();
      await expect
        .poll(() =>
          video.evaluate((element: HTMLVideoElement) => element.muted),
        )
        .toBe(false);

      const volume = player.getByRole("group", { name: "Volume", exact: true });
      if ((await player.boundingBox())!.width >= 408) {
        await expect(volume).toBeInViewport({ ratio: 1 });
        const slider = player.getByRole("slider");
        await slider.focus();
        await slider.press("Home");
        await expect
          .poll(() =>
            video.evaluate((element: HTMLVideoElement) => element.volume),
          )
          .toBe(0);
        await slider.press("ArrowRight");
        await expect
          .poll(() =>
            video.evaluate((element: HTMLVideoElement) => element.volume),
          )
          .toBeGreaterThan(0);
        await expect
          .poll(() =>
            video.evaluate((element: HTMLVideoElement) => element.muted),
          )
          .toBe(false);
      } else {
        await expect(volume).toBeHidden();
      }
    });
  }
});
