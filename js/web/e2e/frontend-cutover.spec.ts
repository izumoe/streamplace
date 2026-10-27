import { loginThroughPds } from "../../e2e-web/flows/login";
import { expect, test } from "./fixtures";

const server = process.env.SERVER_URL;
const httpsServer = process.env.SERVER_HTTPS_URL;
const handle = process.env.ACCOUNT_HANDLE;

test.describe("embedded web frontend", () => {
  test.skip(!server || !handle, "requires hack/e2e-web-local.sh");

  test("home and settings use the DOM app regardless of old beta cookies", async ({
    page,
    context,
  }) => {
    for (const value of [null, "0", "1", "garbage"]) {
      await context.clearCookies();
      if (value !== null) {
        await context.addCookies([
          { name: "sp_web_beta", value, url: server! },
        ]);
      }
      const response = await page.goto(`${server}/`);
      expect(response?.ok()).toBe(true);
      const html = await response!.text();
      expect(html).toMatch(/<script[^>]+type="module"[^>]+src="/);
      expect(html).not.toContain("_expo/static/js/web");
      await expect(
        page
          .getByRole("link")
          .filter({ hasText: `@${handle}` })
          .first(),
      ).toBeVisible({ timeout: 60_000 });
      await expect(page.locator("header")).toBeVisible();

      await page.goto(`${server}/settings/advanced`);
      await expect(
        page.getByRole("heading", { name: "Advanced", exact: true }),
      ).toBeVisible();
      await expect(
        page.getByText("Try the new web (beta)", { exact: true }),
      ).toHaveCount(0);
      await expect(page.getByRole("switch")).toHaveCount(1);
    }
    const worker = await page.request.get(`${server}/sw.js`);
    expect(worker.ok()).toBe(true);
    expect(worker.headers()["content-type"]).toMatch(
      /^(?:text|application)\/javascript/,
    );
    await expect
      .poll(() =>
        page.evaluate(async () => {
          const registration = await navigator.serviceWorker.getRegistration();
          return registration?.active?.scriptURL;
        }),
      )
      .toBe(`${server}/sw.js`);
  });

  test("opening the home card plays the local account's real stream", async ({
    page,
  }) => {
    await page.addInitScript(() => {
      localStorage.setItem("player-transport", "hls");
    });
    await page.goto(`${server}/`);
    const card = page
      .getByRole("link")
      .filter({ hasText: `@${handle}` })
      .first();
    await expect(card).toBeVisible({ timeout: 60_000 });
    await card.click();
    await expect
      .poll(() => decodeURIComponent(new URL(page.url()).pathname))
      .toBe(`/${handle}`);
    const video = page.locator("video").first();
    await expect(video).toBeVisible();
    await expect
      .poll(() =>
        video.evaluate((element: HTMLVideoElement) => element.currentTime),
      )
      .toBeGreaterThan(0);
  });

  test("PDS login and persisted site branding work in the DOM app", async ({
    page,
    request,
  }) => {
    test.skip(!httpsServer, "requires the harness HTTPS hostnames");
    test.setTimeout(4 * 60_000);
    const brandingResponse = await request.get(
      `${server}/xrpc/place.stream.branding.getBranding`,
    );
    expect(brandingResponse.ok()).toBe(true);
    const branding = (await brandingResponse.json()) as {
      assets: Array<{ key: string; data?: string }>;
    };
    const originalTitle = branding.assets.find(
      (asset) => asset.key === "siteTitle",
    )?.data;
    expect(typeof originalTitle).toBe("string");

    await loginThroughPds(page);

    try {
      await page.goto(`${httpsServer}/settings/branding`);
      const title = page.getByPlaceholder("Enter new site title");
      await title.fill("browser cutover branding");
      const saved = page.waitForResponse((response) =>
        response.url().endsWith("/xrpc/place.stream.branding.updateBlob"),
      );
      await title
        .locator("..")
        .getByRole("button", { name: "Update", exact: true })
        .click();
      expect((await saved).ok()).toBe(true);

      await page.goto(`${httpsServer}/`);
      await expect(page).toHaveTitle("browser cutover branding");
    } finally {
      // The text editor only saves nonempty values. Restore the exact baseline through the real admin API.
      const pds = process.env.PDS_HTTP_URL!;
      const session = await request.post(
        `${pds}/xrpc/com.atproto.server.createSession`,
        {
          data: {
            identifier: process.env.ACCOUNT_DID!,
            password: process.env.ACCOUNT_PASSWORD!,
          },
        },
      );
      expect(session.ok()).toBe(true);
      const { accessJwt } = await session.json();
      const document = await request.get(`${server}/.well-known/did.json`);
      expect(document.ok()).toBe(true);
      const { id } = await document.json();
      const method = originalTitle
        ? "place.stream.branding.updateBlob"
        : "place.stream.branding.deleteBlob";
      const auth = await request.get(
        `${pds}/xrpc/com.atproto.server.getServiceAuth`,
        {
          params: { aud: id, lxm: method },
          headers: { Authorization: `Bearer ${accessJwt}` },
        },
      );
      expect(auth.ok()).toBe(true);
      const { token } = await auth.json();
      const response = await request.post(`${server}/xrpc/${method}`, {
        headers: { Authorization: `Bearer ${token}` },
        data: originalTitle
          ? {
              key: "siteTitle",
              data: Buffer.from(originalTitle).toString("base64"),
              mimeType: "text/plain",
            }
          : { key: "siteTitle" },
      });
      expect(originalTitle ? [200] : [200, 404]).toContain(response.status());
      const restored = await request.get(
        `${server}/xrpc/place.stream.branding.getBranding`,
      );
      expect(restored.ok()).toBe(true);
      const after = await restored.json();
      expect(
        after.assets.find((asset: { key: string }) => asset.key === "siteTitle")
          ?.data,
      ).toBe(originalTitle);
    }
  });
});
