import { expect, test } from "./fixtures";

test("a tall about section does not shrink the live player", async ({
  page,
  request,
}, testInfo) => {
  const server = process.env.SERVER_HTTPS_URL!;
  const node = process.env.SERVER_URL!;
  const pds = process.env.PDS_HTTP_URL!;
  const did = process.env.ACCOUNT_DID!;
  const session = await request.post(
    `${pds}/xrpc/com.atproto.server.createSession`,
    {
      data: {
        identifier: process.env.ACCOUNT_HANDLE,
        password: process.env.ACCOUNT_PASSWORD,
      },
    },
  );
  expect(session.ok()).toBe(true);
  const headers = {
    Authorization: `Bearer ${(await session.json()).accessJwt}`,
  };
  const collection = "place.stream.bio.page";
  const description =
    "Layout profile fixture\n" + "More about this streamer.\n".repeat(60);
  const published = await request.post(
    `${pds}/xrpc/com.atproto.repo.putRecord`,
    {
      headers,
      data: {
        repo: did,
        collection,
        rkey: "self",
        record: {
          $type: collection,
          createdAt: new Date().toISOString(),
          description: { plaintext: description },
        },
      },
    },
  );
  expect(published.ok()).toBe(true);
  try {
    await expect
      .poll(async () => {
        const response = await request.get(
          `${node}/xrpc/place.stream.bio.getPage`,
          { params: { repo: did } },
        );
        return response.ok()
          ? (await response.json()).description?.plaintext
          : null;
      })
      .toBe(description);
    await page.addInitScript((node) => {
      localStorage.setItem("streamplaceUrl", node);
      localStorage.setItem("player-transport", "hls");
    }, server);
    await page.goto(`${server}/${did}`, { waitUntil: "domcontentloaded" });
    const player = page.locator("[data-playback-transport]");
    const profile = page.getByRole("region", {
      name: "Profile",
      exact: true,
      includeHidden: true,
    });
    for (const viewport of [
      { width: 1280, height: 720 },
      { width: 390, height: 844 },
    ]) {
      await page.setViewportSize(viewport);
      if (viewport.width < viewport.height) {
        const closeChat = page
          .getByRole("button", { name: "Close chat", exact: true })
          .filter({ visible: true });
        if (await closeChat.count()) await closeChat.first().click();
      }
      await expect(profile).toContainText("Layout profile fixture");
      await expect(profile).toBeVisible();
      await expect
        .poll(async () => {
          const bounds = (await player.boundingBox())!;
          const ratio = await player
            .locator("video")
            .evaluate(
              (video: HTMLVideoElement) => video.videoWidth / video.videoHeight,
            );
          return (
            bounds.height -
            (viewport.width > viewport.height
              ? Math.min(viewport.width / ratio, viewport.height - 240)
              : bounds.width / ratio)
          );
        })
        .toBeCloseTo(0, 0);
      const withProfile = (await player.boundingBox())!.height;
      await profile.evaluate((element) => {
        element.style.display = "none";
      });
      const withoutProfile = (await player.boundingBox())!.height;
      await profile.evaluate((element) => {
        element.style.removeProperty("display");
      });
      expect(withProfile).toBeGreaterThanOrEqual(withoutProfile - 1);
      const frames = () =>
        player
          .locator("video")
          .evaluate(
            (video: HTMLVideoElement) =>
              video.getVideoPlaybackQuality().totalVideoFrames,
          );
      const before = await frames();
      await expect.poll(frames).toBeGreaterThan(before);
      await player.scrollIntoViewIfNeeded();
      await page.screenshot({
        path: testInfo.outputPath(`about-player-${viewport.width}.png`),
      });
      await profile.scrollIntoViewIfNeeded();
      await expect(profile).toBeInViewport();
    }
  } finally {
    const removed = await request.post(
      `${pds}/xrpc/com.atproto.repo.deleteRecord`,
      {
        headers,
        data: { repo: did, collection, rkey: "self" },
      },
    );
    expect(removed.ok()).toBe(true);
  }
});
