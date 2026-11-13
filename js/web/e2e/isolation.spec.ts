import { expect, test } from "@playwright/test";
import { loginThroughPds } from "../../e2e-web/flows/login";

test("document isolation preserves real PDS login and cross-origin popup messaging", async ({
  page,
}) => {
  test.setTimeout(180_000);
  test.skip(
    !process.env.SERVER_HTTPS_URL,
    "requires the local TLS PDS harness",
  );
  await page.addInitScript(
    (url) => localStorage.setItem("streamplaceUrl", url),
    process.env.SERVER_HTTPS_URL!,
  );
  await loginThroughPds(page);
  expect(await page.evaluate(() => crossOriginIsolated)).toBe(true);
  await page.reload();
  await expect(
    page.locator('header a[href="/settings/account"]'),
  ).toBeVisible();
  const popupEvent = page.waitForEvent("popup");
  await page.evaluate(
    (url) => window.open(url, "isolation-popup"),
    process.env.PDS_HTTPS_URL!,
  );
  const popup = await popupEvent;
  await popup.waitForLoadState();
  expect(await popup.evaluate(() => window.opener !== null)).toBe(true);
  const reply = page.evaluate(
    () =>
      new Promise<string>((resolve) => {
        window.addEventListener(
          "message",
          (event) => {
            if (event.data === "isolation-popup-ready") resolve(event.data);
          },
          { once: true },
        );
      }),
  );
  await popup.evaluate(() =>
    window.opener.postMessage("isolation-popup-ready", "*"),
  );
  expect(await reply).toBe("isolation-popup-ready");
  await popup.close();
});

test("the player remains embeddable by a non-isolated external page", async ({
  page,
}) => {
  const server = process.env.SERVER_HTTPS_URL!;
  const did = process.env.ACCOUNT_DID!;
  test.skip(!server || !did, "requires the live local harness");
  await page.addInitScript(
    (url) => localStorage.setItem("streamplaceUrl", url),
    server,
  );
  await page.goto(process.env.PDS_HTTPS_URL!);
  await page.evaluate(
    (src) => {
      const iframe = document.createElement("iframe");
      iframe.src = src;
      iframe.width = "960";
      iframe.height = "540";
      document.body.append(iframe);
    },
    `${server}/embed/${encodeURIComponent(did)}/`,
  );
  const frame = page.frameLocator("iframe");
  const player = frame.locator("[data-playback-transport]");
  await expect(player).toHaveAttribute("data-playback-transport", "moq");
  await expect(player.locator("canvas")).toHaveCSS("opacity", "1");
  const picture = () =>
    player
      .locator("canvas")
      .evaluate((canvas: HTMLCanvasElement) => canvas.toDataURL());
  const first = await picture();
  await expect.poll(picture).not.toBe(first);
});
