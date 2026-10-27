import { expect, test } from "@playwright/test";
import { withInvalidHandle } from "./invalid-handle-fixture";

// Shared behavior: .maestro/logged-out/invalid-handle.yaml. A real unverifiable
// handle must leave its account reachable by DID, without displaying a fake handle.
const DID = process.env.ACCOUNT_DID!;
test.setTimeout(150_000);

test("invalid handle: home and playback remain DID-addressable", async ({
  page,
  context,
  baseURL,
}) => {
  const origin = process.env.SERVER_HTTPS_URL || baseURL!;
  await page.addInitScript(() => {
    localStorage.setItem("player-transport", "hls");
  });
  await withInvalidHandle(async () => {
    await page.goto(`${origin}/`);
    const card = page
      .getByRole("link")
      .filter({ hasText: "e2e test stream" })
      .first();
    await expect(card).toContainText(DID);
    await expect(card).not.toContainText("handle.invalid");
    await expect(card).not.toContainText(`@${DID}`);
    await card.click();
    await expect
      .poll(() => decodeURIComponent(new URL(page.url()).pathname))
      .toBe(`/${DID}`);
    const video = page.locator("video").first();
    await expect(video).toBeVisible();
    await expect
      .poll(() =>
        video.evaluate((element: HTMLVideoElement) => element.currentTime),
      )
      .toBeGreaterThan(0);
    await expect(page.getByText("handle.invalid", { exact: true })).toHaveCount(
      0,
    );
    await expect(page.locator('a[href*="handle.invalid"]')).toHaveCount(0);

    if (process.env.SERVER_HTTPS_URL) {
      await context.grantPermissions(["clipboard-read", "clipboard-write"], {
        origin: new URL(origin).origin,
      });
      await page.getByRole("button", { name: "Copy", exact: true }).click();
      await page
        .getByRole("menuitem", { name: "Copy Link", exact: true })
        .click();
      await expect
        .poll(() => page.evaluate(() => navigator.clipboard.readText()))
        .toBe(`${origin}/${DID}`);
    }

    // The real node excludes unverifiable handles from handle search.
    await page.goto(`${origin}/search`);
    const results = page.waitForResponse(
      (response) =>
        new URL(response.url()).pathname ===
          "/xrpc/place.stream.live.searchActorsTypeahead" &&
        new URL(response.url()).searchParams.get("q") === "handle.invalid",
    );
    await page.getByRole("searchbox").fill("handle.invalid");
    const response = await results;
    expect(response.ok()).toBe(true);
    expect((await response.json()).actors).toEqual([]);
    await expect(page.getByRole("link").filter({ hasText: DID })).toHaveCount(
      0,
    );
  });
});
