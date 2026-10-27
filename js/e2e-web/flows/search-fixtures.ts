import type { Page } from "@playwright/test";

const SEARCH_URL = "**/xrpc/place.stream.live.searchActorsTypeahead*";
const queryOf = (url: string) =>
  new URL(url).searchParams.get("q")?.toLowerCase();

// Delay the real node response; never replace its results.
export async function holdQuery(page: Page, query: string) {
  let release!: () => void;
  const released = new Promise<void>((resolve) => (release = resolve));
  let seen!: () => void;
  const requested = new Promise<void>((resolve) => (seen = resolve));
  await page.route(SEARCH_URL, async (route) => {
    if (queryOf(route.request().url()) === query.toLowerCase()) {
      seen();
      await released;
    }
    await route.continue();
  });
  return { requested, release };
}

export async function deliver(page: Page, query: string, release: () => void) {
  const response = page.waitForResponse(
    (r) => queryOf(r.url()) === query.toLowerCase(),
  );
  release();
  await response;
}
