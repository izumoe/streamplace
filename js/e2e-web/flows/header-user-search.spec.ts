import { expect, request, test, type Page } from "@playwright/test";
import { deliver, holdQuery } from "./search-fixtures";

const HANDLE = process.env.ACCOUNT_HANDLE!;
const PREFIX = HANDLE.split(".")[0].toUpperCase();
const input = (page: Page) => page.getByRole("combobox", { name: "Search..." });
const dropdown = (page: Page) => page.getByRole("listbox");
const result = (page: Page) =>
  page.getByRole("option").filter({ hasText: HANDLE });

// All search results come from the real harness node, including delayed replies.
test("header-user-search: typing, clearing and selecting work without leaving the current page", async ({
  page,
}) => {
  await page.goto("/settings/advanced");
  await input(page).fill(`nobody-${Date.now()}`);
  await expect(dropdown(page).getByText(/No results for/)).toBeVisible();
  await input(page).fill("");
  await expect(dropdown(page)).toBeHidden();
  await input(page).fill(` @${PREFIX} `);
  await expect(result(page)).toBeVisible();
  await expect(page).toHaveURL(/\/settings\/advanced$/);
  await result(page).click();
  await expect
    .poll(() => decodeURIComponent(new URL(page.url()).pathname))
    .toBe(`/${HANDLE}`);
  await expect(dropdown(page)).toBeHidden();
  await expect(
    page.getByText("e2e test stream", { exact: true }).first(),
  ).toBeVisible();
});

test("header-user-search: Escape and outside presses close results while page links remain usable", async ({
  page,
}) => {
  await page.goto("/settings/advanced");
  await input(page).fill(PREFIX);
  await expect(result(page)).toBeVisible();
  await input(page).press("Escape");
  await expect(dropdown(page)).toBeHidden();
  await input(page).fill(PREFIX);
  await expect(result(page)).toBeVisible();
  await page.getByRole("link", { name: "About", exact: true }).click();
  await expect(dropdown(page)).toBeHidden();
  await expect(page).toHaveURL(/\/settings\/about$/);
  await input(page).fill("");
  await page.getByRole("link", { name: "Home", exact: true }).first().click();
  await expect(page).toHaveURL(/\/$/);
});

test("header-user-search: More results opens the full search with the same query", async ({
  page,
}) => {
  await page.goto("/settings/advanced");
  await input(page).fill(PREFIX);
  await expect(result(page)).toBeVisible();
  await dropdown(page).getByRole("link", { name: "More results" }).click();
  await expect(page).toHaveURL(/\/search\?q=/);
  await expect(
    page.getByRole("searchbox", { name: "Search for streamers..." }),
  ).toHaveValue(PREFIX);
  await expect(
    page.getByRole("link").filter({ hasText: HANDLE }),
  ).toBeVisible();
});

test("header-user-search: latest query wins over a delayed response", async ({
  page,
}) => {
  const slow = await holdQuery(page, PREFIX);
  await page.goto("/settings/advanced");
  await input(page).fill(PREFIX);
  await slow.requested;
  await input(page).fill(`nobody-${Date.now()}`);
  await expect(dropdown(page).getByText(/No results for/)).toBeVisible();
  await deliver(page, PREFIX, slow.release);
  await expect(result(page)).toBeHidden();
  await expect(dropdown(page).getByText(/No results for/)).toBeVisible();
});

for (const how of ["clearing the field", "pressing Escape"]) {
  test(`header-user-search: a delayed response after ${how} stays hidden`, async ({
    page,
  }) => {
    const slow = await holdQuery(page, PREFIX);
    await page.goto("/settings/advanced");
    await input(page).fill(PREFIX);
    await slow.requested;
    if (how === "pressing Escape") await input(page).press("Escape");
    else await input(page).fill("");
    await deliver(page, PREFIX, slow.release);
    await expect(dropdown(page)).toBeHidden();
    await expect(result(page)).toBeHidden();
  });
}

test("header-user-search: real accounts can be highlighted and opened with the keyboard", async ({
  page,
}) => {
  const pdsUrl = process.env.PDS_HTTPS_URL;
  test.skip(!pdsUrl, "harness started without its HTTPS hostnames");
  const pds = await request.newContext({ ignoreHTTPSErrors: true });
  const prefix = `srch${Date.now().toString(36).slice(-6)}`;
  const records: { did: string; accessJwt: string; rkey: string }[] = [];
  try {
    for (let i = 0; i < 3; i++) {
      const handle = `${prefix}${i}.${new URL(pdsUrl!).hostname}`;
      const created = await pds.post(
        `${pdsUrl}/xrpc/com.atproto.server.createAccount`,
        {
          data: {
            handle,
            email: `${prefix}${i}@example.com`,
            password: process.env.ACCOUNT_PASSWORD!,
          },
        },
      );
      expect(created.ok(), await created.text()).toBe(true);
      const { did, accessJwt } = await created.json();
      const posted = await pds.post(
        `${pdsUrl}/xrpc/com.atproto.repo.createRecord`,
        {
          headers: { Authorization: `Bearer ${accessJwt}` },
          data: {
            repo: did,
            collection: "place.stream.chat.message",
            record: {
              $type: "place.stream.chat.message",
              text: "search fixture",
              createdAt: new Date().toISOString(),
              streamer: process.env.ACCOUNT_DID!,
            },
          },
        },
      );
      expect(posted.ok(), await posted.text()).toBe(true);
      records.push({
        did,
        accessJwt,
        rkey: (await posted.json()).uri.split("/").pop(),
      });
    }
    await page.goto("/settings/advanced");
    await expect
      .poll(async () => {
        const response = await page.request.get(
          `/xrpc/place.stream.live.searchActorsTypeahead?q=${prefix}`,
        );
        return (await response.json()).actors.length;
      })
      .toBe(3);
    await input(page).fill(prefix);
    const options = dropdown(page).getByRole("option");
    await expect(options).toHaveCount(3);
    const selectedHandle = (await options.nth(1).innerText())
      .split("\n")
      .pop()!
      .replace(/^@/, "");
    await input(page).press("ArrowDown");
    await expect(options.nth(0)).toHaveAttribute("aria-selected", "true");
    await input(page).press("ArrowDown");
    await expect(options.nth(1)).toHaveAttribute("aria-selected", "true");
    await expect(input(page)).toHaveAttribute(
      "aria-activedescendant",
      (await options.nth(1).getAttribute("id")) as string,
    );
    await expect(options.nth(1)).toBeInViewport();
    await input(page).press("Enter");
    await expect
      .poll(() => decodeURIComponent(new URL(page.url()).pathname))
      .toBe(`/${selectedHandle}`);
    await expect(dropdown(page)).toBeHidden();
  } finally {
    for (const record of records) {
      const deleted = await pds.post(
        `${pdsUrl}/xrpc/com.atproto.repo.deleteRecord`,
        {
          headers: { Authorization: `Bearer ${record.accessJwt}` },
          data: {
            repo: record.did,
            collection: "place.stream.chat.message",
            rkey: record.rkey,
          },
        },
      );
      expect(deleted.ok(), await deleted.text()).toBe(true);
    }
    await pds.dispose();
  }
});

test("header-user-search: search remains usable on a narrow viewport", async ({
  page,
}) => {
  await page.setViewportSize({ width: 320, height: 568 });
  await page.goto("/settings/advanced");
  await input(page).fill(PREFIX);
  await expect(result(page)).toBeVisible();
  const box = await dropdown(page).boundingBox();
  expect(box!.x).toBeGreaterThanOrEqual(0);
  expect(box!.x + box!.width).toBeLessThanOrEqual(320);
  await input(page).press("ArrowDown");
  await input(page).press("Enter");
  await expect
    .poll(() => decodeURIComponent(new URL(page.url()).pathname))
    .toBe(`/${HANDLE}`);
});
