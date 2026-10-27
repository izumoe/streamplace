import {
  expect,
  request,
  test,
  type Locator,
  type Page,
} from "@playwright/test";

// Small trackpad deltas must move through real chat history in both directions.
// Records are written to the local PDS; this also runs in Firefox.
const SERVER_URL = process.env.SERVER_URL;
const PDS_URL = process.env.PDS_HTTPS_URL;
const HANDLE = process.env.ACCOUNT_HANDLE!;
const DID = process.env.ACCOUNT_DID!;
const PASSWORD = process.env.ACCOUNT_PASSWORD!;

test.skip(!PDS_URL, "harness started without its HTTPS hostnames");

const MESSAGES = 30;
const STEPS = 40;
const STEP_PX = 0.5;

const scrollTop = (list: Locator) => list.evaluate((el) => el.scrollTop);

// Write chat messages to the harness account's PDS; the node picks them up
// from its firehose, as it would any viewer's.
async function seedChat(texts: string[]) {
  // the PDS's throwaway CA is trusted by the harness's browsers, not by Node
  const pds = await request.newContext({ ignoreHTTPSErrors: true });
  try {
    const session = await pds.post(
      `${PDS_URL}/xrpc/com.atproto.server.createSession`,
      { data: { identifier: HANDLE, password: PASSWORD } },
    );
    expect(session.ok(), `createSession: ${session.status()}`).toBe(true);
    const { accessJwt } = await session.json();
    for (const text of texts) {
      const res = await pds.post(
        `${PDS_URL}/xrpc/com.atproto.repo.createRecord`,
        {
          headers: { Authorization: `Bearer ${accessJwt}` },
          data: {
            repo: DID,
            collection: "place.stream.chat.message",
            record: {
              $type: "place.stream.chat.message",
              text,
              createdAt: new Date().toISOString(),
              streamer: DID,
            },
          },
        },
      );
      expect(res.ok(), `createRecord: ${res.status()}`).toBe(true);
    }
  } finally {
    await pds.dispose();
  }
}

async function wheelSteps(page: Page, deltaY: number, steps: number) {
  for (let i = 0; i < steps; i++) {
    await page.mouse.wheel(0, deltaY);
  }
}

test("chat-wheel-scroll: small wheel deltas scroll chat both ways", async ({
  page,
}) => {
  const run = Date.now();
  const texts = Array.from(
    { length: MESSAGES },
    (_, i) => `wheel scroll ${run} ${i}`,
  );
  await seedChat(texts);

  await page.setViewportSize({ width: 400, height: 400 });
  await page.goto(`${SERVER_URL}/chat-popout/${DID}`);
  await expect(
    page.getByText(texts[texts.length - 1], { exact: true }).first(),
  ).toBeVisible({ timeout: 30_000 });

  const list = page
    .getByText(texts[texts.length - 1], { exact: true })
    .locator("xpath=ancestor::div[contains(@class, 'overflow-y-auto')]");
  await expect
    .poll(() => list.evaluate((el) => el.scrollHeight - el.clientHeight))
    .toBeGreaterThan(200);

  const box = await list.boundingBox();
  expect(box).not.toBeNull();
  await page.mouse.move(box!.x + box!.width / 2, box!.y + box!.height / 2);

  const atLatest = await scrollTop(list);
  await page.mouse.wheel(0, -150);
  await expect.poll(() => scrollTop(list)).toBeLessThan(atLatest - 100);
  const inHistory = await scrollTop(list);

  await wheelSteps(page, STEP_PX, STEPS);
  await expect.poll(() => scrollTop(list)).toBeGreaterThan(inHistory + 10);
  const afterDown = await scrollTop(list);
  await wheelSteps(page, -STEP_PX, STEPS);
  await expect.poll(() => scrollTop(list)).toBeLessThan(afterDown - 10);

  await page
    .getByRole("button", { name: "Scroll to bottom", exact: true })
    .click();
  await expect
    .poll(async () =>
      list.evaluate((el) => el.scrollHeight - el.clientHeight - el.scrollTop),
    )
    .toBeLessThan(2);
});
