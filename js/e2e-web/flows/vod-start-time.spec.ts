import { expect, test, type Page } from "@playwright/test";
import { loginThroughPds } from "./login";

// VOD links carry a playback start position as `?t=` (YouTube-style: plain
// seconds, or an `1h2m3s` duration). Point it at a video with real media and
// the player must open there instead of at 0:00.
//
// The harness's own test VOD has no source tracks, so this uploads the file
// the harness is streaming (E2E_FIXTURE_MP4) through /dashboard/upload
// workflow first, giving the player something seekable. The
// upload needs a signed-in user, so this needs the harness's HTTPS mode, like
// the other OAuth flows.
const HTTPS_URL = process.env.SERVER_HTTPS_URL;
const ACCOUNT_DID = process.env.ACCOUNT_DID;
const FIXTURE = process.env.E2E_FIXTURE_MP4;
const START_SECONDS = 151;

test.skip(!HTTPS_URL, "harness started without its HTTPS hostnames");
test.skip(!FIXTURE, "harness did not export E2E_FIXTURE_MP4");

// Upload and publish through the real dashboard, then open the resulting VOD.
async function uploadFixture(page: Page): Promise<string> {
  await loginThroughPds(page);
  await page.goto(`${HTTPS_URL}/dashboard/upload`);
  await page
    .getByPlaceholder("Give your video a title")
    .fill(`t-param test video ${Date.now()}`);
  await page.setInputFiles(
    'input[type="file"][accept="video/*"]',
    FIXTURE as string,
  );

  const uploaded = page.waitForResponse(
    (response) =>
      response.request().method() === "PATCH" &&
      new URL(response.url()).pathname.startsWith("/api/upload/"),
    { timeout: 2 * 60_000 },
  );
  await page.getByRole("button", { name: "Upload", exact: true }).click();
  expect((await uploaded).ok()).toBe(true);

  // The dashboard polls the node's processing state and enables publication.
  const publish = page.getByRole("button", { name: "Publish", exact: true });
  await expect(publish).toBeEnabled({ timeout: 8 * 60_000 });

  const published = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      new URL(response.url()).pathname ===
        "/xrpc/place.stream.media.publishVideo",
  );
  await publish.click();
  const response = await published;
  expect(response.ok()).toBe(true);
  const { uri } = (await response.json()) as {
    uri: string;
  };

  const match = uri.match(/^at:\/\/([^/]+)\/place\.stream\.video\/([^/]+)$/);
  if (!match) {
    throw new Error(`publishVideo returned an unexpected uri: ${uri}`);
  }
  const indexedVideo = new URL(
    "/xrpc/place.stream.media.getVideo",
    process.env.SERVER_URL,
  );
  indexedVideo.searchParams.set("uri", uri);
  // Publication fills the source with canonical track records, even though
  // the processing status returned no published references yet.
  await expect(async () => {
    const response = await page.request.get(indexedVideo.href);
    expect(response.ok()).toBe(true);
    const { record } = await response.json();
    expect(record.source.$type).toBe("place.stream.media.defs#sourceTracks");
    expect(record.source.tracks.length).toBeGreaterThan(0);
    for (const track of record.source.tracks) {
      expect(
        track.uri.startsWith(`at://${ACCOUNT_DID}/place.stream.media.track/`),
      ).toBe(true);
      expect(track.cid).toEqual(expect.any(String));
      expect(track.cid.length).toBeGreaterThan(0);
    }
  }).toPass({ timeout: 30_000 });
  return `/${match[1]}/video/${match[2]}`;
}

test("vod-start-time: ?t= starts playback partway into a VOD", async ({
  page,
}) => {
  // Uploading and transcoding the fixture can outlast the suite's default 90s
  // test timeout on a loaded CI runner.
  test.setTimeout(10 * 60_000);

  const watchPath = await uploadFixture(page);
  expect(watchPath).toContain(ACCOUNT_DID as string);

  await page.goto(`${HTTPS_URL}${watchPath}?t=${START_SECONDS}`);
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

  // The player opens at the requested position (the fragment containing it,
  // so the exact frame is a little after it), not at 0:00.
  await expect
    .poll(
      () => video.evaluate((element: HTMLVideoElement) => element.currentTime),
      { timeout: 60_000 },
    )
    .toBeGreaterThanOrEqual(START_SECONDS);
});

test("vod-start-time: playback recovers after a media request fails", async ({
  page,
}) => {
  test.setTimeout(10 * 60_000);
  const watchPath = await uploadFixture(page);
  let attempts = 0;
  let failedRequest: { url: string; range: string } | undefined;
  await page.route(
    "**/xrpc/place.stream.playback.getVideoBlob?**",
    async (route) => {
      const request = route.request();
      const range = request.headers().range;
      if (range && !failedRequest)
        failedRequest = { url: request.url(), range };
      if (
        request.url() === failedRequest?.url &&
        range === failedRequest.range
      ) {
        attempts++;
        if (attempts === 1) return route.abort("connectionreset");
      }
      await route.continue();
    },
  );

  await page.goto(`${HTTPS_URL}${watchPath}?t=${START_SECONDS}`);
  const video = page.locator("video").first();
  await expect(video).toBeVisible({ timeout: 30_000 });
  await expect.poll(() => attempts, { timeout: 30_000 }).toBeGreaterThan(1);
  await expect
    .poll(() =>
      video.evaluate(
        (element: HTMLVideoElement) =>
          element.getVideoPlaybackQuality().totalVideoFrames,
      ),
    )
    .toBeGreaterThan(0);
  await expect
    .poll(
      () =>
        video.evaluate(
          (element: HTMLVideoElement, startSeconds: number) => ({
            paused: element.paused,
            progressed: element.currentTime > startSeconds + 1,
          }),
          START_SECONDS,
        ),
      { timeout: 60_000 },
    )
    .toEqual({ paused: false, progressed: true });
});
