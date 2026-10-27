import { expect, test } from "@playwright/test";
import { loginThroughPds } from "./login";

// Native counterpart: .maestro/logged-in/chat-timezone.yaml. Browser coverage
// checks the stored instant and the viewer's local clock, including a fractional zone.
const HTTPS_URL = process.env.SERVER_HTTPS_URL;
test.skip(!HTTPS_URL, "harness started without its HTTPS hostnames");

for (const timezoneId of ["America/Los_Angeles", "Asia/Kathmandu", "UTC"]) {
  test.describe(timezoneId, () => {
    test.use({ timezoneId, locale: "en-US" });

    test("chat stores the sending instant and displays local time", async ({
      page,
      request,
    }) => {
      await loginThroughPds(page);
      // Newest-first keeps this message's timestamp visible even when older
      // messages from this account would otherwise group it with their author.
      await page.goto(
        `${HTTPS_URL}/chat-popout/${process.env.ACCOUNT_DID}?reverse=true`,
      );
      await expect(
        page.getByText("Now streaming - e2e test stream").first(),
      ).toBeVisible();

      const message = `timezone check ${timezoneId} ${Date.now()}`;
      const responsePromise = page.waitForResponse((response) => {
        const request = response.request();
        return (
          request.method() === "POST" &&
          new URL(response.url()).pathname ===
            "/xrpc/com.atproto.repo.createRecord" &&
          request.postData()?.includes(message) === true
        );
      });
      const before = Date.now();
      const input = page.locator('[contenteditable="true"]');
      await input.fill(message);
      await input.press("Enter");
      const response = await responsePromise;
      expect(response.ok()).toBe(true);
      const { uri } = (await response.json()) as { uri: string };
      const record = await request.get(
        `${process.env.PDS_HTTP_URL}/xrpc/com.atproto.repo.getRecord`,
        {
          params: {
            repo: process.env.ACCOUNT_DID!,
            collection: "place.stream.chat.message",
            rkey: uri.slice(uri.lastIndexOf("/") + 1),
          },
        },
      );
      expect(record.ok()).toBe(true);
      const { createdAt } = (await record.json()).value;
      expect(typeof createdAt).toBe("string");
      expect(Date.parse(createdAt)).toBeGreaterThanOrEqual(before);
      expect(Date.parse(createdAt)).toBeLessThanOrEqual(Date.now());
      const localTime = new Intl.DateTimeFormat("en-US", {
        timeZone: timezoneId,
        hour: "2-digit",
        minute: "2-digit",
        hour12: false,
      }).format(new Date(createdAt));
      const row = page.locator("div.group").filter({ hasText: message });
      await expect(row).toBeVisible();
      await expect(row.getByText(localTime, { exact: true })).toBeVisible();

      // Reloading removes the optimistic echo; history must retain both text and time.
      await page.reload();
      await expect(row).toBeVisible();
      await expect(row.getByText(localTime, { exact: true })).toBeVisible();
    });
  });
}
