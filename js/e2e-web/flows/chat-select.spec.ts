import {
  expect,
  request,
  test,
  type Locator,
  type Page,
} from "@playwright/test";

// Chat is filled by writing records straight to the account's PDS, so this
// flow needs no browser login.
const SERVER_URL = process.env.SERVER_URL;
const PDS_URL = process.env.PDS_HTTPS_URL;
const HANDLE = process.env.ACCOUNT_HANDLE!;
const DID = process.env.ACCOUNT_DID!;
const PASSWORD = process.env.ACCOUNT_PASSWORD!;

test.skip(!PDS_URL, "harness started without its HTTPS hostnames");

async function seedChat(texts: string[], link: string) {
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
              ...(text.includes(link)
                ? {
                    facets: [
                      {
                        $type: "place.stream.richtext.facet",
                        index: {
                          byteStart: text.indexOf(link),
                          byteEnd: text.indexOf(link) + link.length,
                        },
                        features: [
                          {
                            $type: "app.bsky.richtext.facet#link",
                            uri: link,
                          },
                        ],
                      },
                    ],
                  }
                : {}),
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

// Drag across a text element's first line and return the browser selection.
async function dragSelect(page: Page, target: Locator): Promise<string> {
  await target.scrollIntoViewIfNeeded();
  const box = await target.boundingBox();
  expect(box, "selection target has a layout box").not.toBeNull();
  const y = box!.y + box!.height / 2;
  await page.mouse.move(box!.x + 1, y);
  await page.mouse.down();
  await page.mouse.move(box!.x + box!.width - 1, y, { steps: 8 });
  await page.mouse.up();
  return page.evaluate(() => window.getSelection()?.toString() ?? "");
}

test("chat-select: a message can be drag-selected into the browser selection", async ({
  page,
}) => {
  const run = Date.now();
  const message = `select me ${run}`;
  const linkText = `selectlink${run}`;
  const uri = `https://example.com/${run}`;
  await seedChat([message, `${linkText} ${uri}`], uri);

  await page.goto(`${SERVER_URL}/chat-popout/${DID}?reverse=true`);

  const body = page.getByText(message, { exact: true }).first();
  await expect(body).toBeVisible({ timeout: 30_000 });

  // Dragging across the body selects the message text.
  const selected = await dragSelect(page, body);
  expect(selected).toContain(message);

  // A real link facet stays part of the message's native browser selection.
  const link = page.getByRole("link", { name: uri, exact: true });
  await expect(link).toBeVisible();
  await expect(link).toHaveAttribute("href", uri);
  const acrossSegments = await link.locator("..").evaluate((scope) => {
    const range = document.createRange();
    range.selectNodeContents(scope);
    const sel = window.getSelection()!;
    sel.removeAllRanges();
    sel.addRange(range);
    return sel.toString();
  });
  expect(acrossSegments).toContain(linkText);
  expect(acrossSegments).toContain("example.com");

  // The selection lives inside the chat list, so the surrounding page chrome
  // is not swept into it.
  const inChat = await link.locator("..").evaluate((message) => {
    const sel = window.getSelection();
    if (!sel || sel.isCollapsed || sel.rangeCount === 0) return false;
    return message.contains(sel.getRangeAt(0).startContainer);
  });
  expect(inChat).toBe(true);
});
