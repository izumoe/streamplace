import { expect, test } from "@playwright/test";
import { loginThroughPds } from "./login";

// A native popout link opens one chat window and leaves playback in its tab.
const HTTPS_URL = process.env.SERVER_HTTPS_URL;

test.skip(!HTTPS_URL, "harness started without its HTTPS hostnames");

test("chat-popout: opens one working chat window, leaves the stream page alone", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1536, height: 900 });
  await loginThroughPds(page);
  await page.goto(`${HTTPS_URL}/`);
  await page
    .getByRole("link")
    .filter({ hasText: "e2e test stream" })
    .first()
    .click();
  await expect(
    page.getByRole("heading", { name: "e2e test stream", exact: true }),
  ).toBeVisible();

  const popout = page.getByRole("link", { name: "Pop out chat", exact: true });
  await expect(popout).toBeVisible();
  await expect(popout).toHaveAttribute("target", "_blank");
  const destination = new URL((await popout.getAttribute("href"))!, page.url())
    .href;
  const streamUrl = page.url();
  const popupPromise = page.waitForEvent("popup");
  await popout.click();
  const popup = await popupPromise;
  await expect(popup).toHaveURL(destination);
  const message = `popout chat ${Date.now()}`;
  const sent = popup.waitForResponse(
    (response) =>
      new URL(response.url()).pathname ===
        "/xrpc/com.atproto.repo.createRecord" &&
      response.request().method() === "POST",
  );
  await popup.getByRole("textbox").fill(message);
  await popup.getByRole("textbox").press("Enter");
  expect((await sent).ok()).toBe(true);
  await expect(popup.getByText(message, { exact: true })).toBeVisible();
  await expect(page).toHaveURL(streamUrl);
  expect(page.context().pages()).toHaveLength(2);
  await popup.close();
  expect(page.context().pages()).toHaveLength(1);
});
