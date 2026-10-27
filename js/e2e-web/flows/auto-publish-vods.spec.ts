import { expect, test, type Locator, type Page } from "@playwright/test";
import { loginThroughPds } from "./login";

// Privacy settings persist to the account's real PDS record for this node.
// The harness account has VOD beta access. Publishing itself is covered by
// pkg/statedb and pkg/atproto; this checks saved consent and concurrent edits.
// Native form and settings: .maestro/logged-in/auto-publish-vods.yaml.
const HTTPS_URL = process.env.SERVER_HTTPS_URL;

test.skip(!HTTPS_URL, "harness started without its HTTPS hostnames");

function setting(page: Page, label: string): Locator {
  return page
    .getByText(label, { exact: true })
    .locator("../..")
    .getByRole("switch");
}

const AUTO_PUBLISH = "Publish my livestreams as VODs automatically";
const RECORDING = `Record my livestreams into VODs on ${new URL(HTTPS_URL || "https://unused.invalid").host}`;

async function setSetting(page: Page, label: string, on: boolean) {
  const toggle = setting(page, label);
  const saved = page.waitForResponse(
    (response) =>
      new URL(response.url()).pathname === "/xrpc/com.atproto.repo.putRecord",
  );
  await toggle.click();
  const response = await saved;
  expect(response.ok(), `putRecord returned ${response.status()}`).toBe(true);
  await expect(toggle).toHaveAttribute("aria-checked", String(on));
  await page.reload();
  await expect(setting(page, label)).toHaveAttribute(
    "aria-checked",
    String(on),
  );
}

test("auto-publish-vods: recording opt-out survives another toggle", async ({
  page,
}) => {
  await loginThroughPds(page);

  await page.goto(`${HTTPS_URL}/settings/privacy`);
  const toggle = setting(page, AUTO_PUBLISH);
  await expect(toggle).toBeVisible({ timeout: 30_000 });
  await expect(toggle).toHaveAttribute("aria-checked", /^(true|false)$/);
  const initial = (await toggle.getAttribute("aria-checked")) === "true";

  const recording = setting(page, RECORDING);
  await expect(recording).toBeVisible();
  const recordingInitiallyOn =
    (await recording.getAttribute("aria-checked")) === "true";
  try {
    await setSetting(page, AUTO_PUBLISH, !initial);
    await setSetting(page, AUTO_PUBLISH, initial);
    if (!recordingInitiallyOn) {
      await setSetting(page, RECORDING, true);
    }

    // An opt-out PUT must finish before another setting can be saved. A second
    // PUT based on the old cached record would silently re-enable recording.
    let releasePut!: () => void;
    const blockedPut = new Promise<void>((resolve) => {
      releasePut = resolve;
    });
    let signalPut!: () => void;
    const putStarted = new Promise<void>((resolve) => {
      signalPut = resolve;
    });
    await page.route(
      "**/xrpc/com.atproto.repo.putRecord",
      async (route) => {
        signalPut();
        await blockedPut;
        await route.continue();
      },
      { times: 1 },
    );
    const saved = page.waitForResponse((response) =>
      new URL(response.url()).pathname.endsWith(
        "/xrpc/com.atproto.repo.putRecord",
      ),
    );
    await recording.click();
    try {
      await putStarted;
      await expect(toggle).toBeDisabled();
    } finally {
      releasePut();
    }
    const response = await saved;
    expect(response.ok(), `putRecord returned ${response.status()}`).toBe(true);
    await expect(toggle).toBeEnabled();
    await setSetting(page, AUTO_PUBLISH, !initial);
    await expect(recording).toHaveAttribute("aria-checked", "false");
  } finally {
    await page.unroute("**/xrpc/com.atproto.repo.putRecord");
    await page.goto(`${HTTPS_URL}/settings/privacy`);
    for (const [label, on] of [
      [AUTO_PUBLISH, initial],
      [RECORDING, recordingInitiallyOn],
    ] as const) {
      const toggle = setting(page, label);
      await expect(toggle).toBeEnabled();
      if ((await toggle.getAttribute("aria-checked")) !== String(on)) {
        await setSetting(page, label, on);
      }
    }
  }
});
