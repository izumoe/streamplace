import { expect, test } from "@playwright/test";
import { deliver, holdQuery } from "./search-fixtures";

const HANDLE = process.env.ACCOUNT_HANDLE!;
const PREFIX = HANDLE.split(".")[0].toUpperCase();
const SEARCH_URL = "**/xrpc/place.stream.live.searchActorsTypeahead*";

// These flows query the real node's indexed harness account while signed out.
test("user-search: find an account by handle prefix and open it", async ({
  page,
}) => {
  await page.goto("/search");
  const input = page.getByRole("searchbox", {
    name: "Search for streamers...",
  });
  const result = page.getByRole("link").filter({ hasText: HANDLE });
  await expect(
    page.getByText("Find streamers by name or handle"),
  ).toBeVisible();
  await input.fill(`nobody-${Date.now()}`);
  await expect(page.getByText(/No results for/)).toBeVisible();
  await input.fill(`  @${PREFIX}  `);
  await expect(result).toBeVisible();
  await input.fill("");
  await expect(result).toBeHidden();
  await expect(
    page.getByText("Find streamers by name or handle"),
  ).toBeVisible();
  await input.fill(PREFIX);
  await expect(result).toBeVisible();
  await result.click();
  await expect
    .poll(() => decodeURIComponent(new URL(page.url()).pathname))
    .toBe(`/${HANDLE}`);
  await expect(
    page.getByText("e2e test stream", { exact: true }).first(),
  ).toBeVisible();
});

test("user-search: narrow viewport", async ({ page }) => {
  await page.setViewportSize({ width: 320, height: 568 });
  await page.goto("/search");
  await page
    .getByRole("searchbox", { name: "Search for streamers..." })
    .fill(PREFIX);
  const result = page.getByRole("link").filter({ hasText: HANDLE });
  await expect(result).toBeVisible();
  const box = await result.boundingBox();
  expect(box!.x).toBeGreaterThanOrEqual(0);
  expect(box!.x + box!.width).toBeLessThanOrEqual(320);
});

test("user-search: a network failure is distinct from no matches and recovers on edit", async ({
  page,
}) => {
  await page.route(SEARCH_URL, (route) => route.abort("failed"));
  await page.goto("/search");
  const input = page.getByRole("searchbox", {
    name: "Search for streamers...",
  });
  await input.fill(PREFIX);
  await expect(page.getByRole("alert")).toHaveText("Search failed");
  await expect(page.getByText(/No results for/)).toBeHidden();
  await page.unroute(SEARCH_URL);
  await input.fill(PREFIX.slice(0, -1));
  await expect(
    page.getByRole("link").filter({ hasText: HANDLE }),
  ).toBeVisible();
  await expect(page.getByRole("alert")).toBeHidden();
});

test("user-search: a slow earlier response cannot replace a newer search", async ({
  page,
}) => {
  const slow = await holdQuery(page, PREFIX);
  await page.goto("/search");
  const input = page.getByRole("searchbox", {
    name: "Search for streamers...",
  });
  await input.fill(PREFIX);
  await slow.requested;
  await input.fill(`nobody-${Date.now()}`);
  await expect(page.getByText(/No results for/)).toBeVisible();
  await deliver(page, PREFIX, slow.release);
  await expect(page.getByRole("link").filter({ hasText: HANDLE })).toBeHidden();
  await expect(page.getByText(/No results for/)).toBeVisible();
});
