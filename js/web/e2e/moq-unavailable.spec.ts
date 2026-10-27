import { expect, test } from "@playwright/test";

test.setTimeout(90_000);

// Disable the browser feature itself while leaving the other transports real.
test.use({
  browserName: "firefox",
  launchOptions: {
    firefoxUserPrefs: {
      "network.webtransport.enabled": false,
      "media.autoplay.default": 0,
    },
  },
});

test("Firefox without WebTransport plays through existing live transports", async ({
  page,
}) => {
  const server = process.env.SERVER_URL;
  const did = process.env.ACCOUNT_DID;
  if (!server || !did) {
    throw new Error("SERVER_URL and ACCOUNT_DID require the local e2e harness");
  }
  await page.addInitScript((url) => {
    localStorage.setItem("streamplaceUrl", url);
    localStorage.setItem("player-transport", "webrtc");
  }, server);
  await page.goto(`/embed/${encodeURIComponent(did)}/`);
  expect(
    await page.evaluate(
      () => typeof (window as unknown as Record<string, unknown>).WebTransport,
    ),
  ).toBe("undefined");
  const player = page.locator("[data-playback-transport]");
  await expect(player).not.toHaveAttribute("data-playback-transport", "moq");
  await expect
    .poll(() =>
      player
        .locator("video")
        .evaluate(
          (element: HTMLVideoElement) =>
            element.getVideoPlaybackQuality().totalVideoFrames,
        ),
    )
    .toBeGreaterThan(0);
});
