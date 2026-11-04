import { loginThroughPds } from "../../e2e-web/flows/login";
import { expect, test } from "./fixtures";

test.use({ trace: "off", video: "off" });

test("profile editing preserves published content, reorders cards and protects concurrent edits", async ({
  page,
  context,
  request,
}, testInfo) => {
  test.skip(!process.env.SERVER_HTTPS_URL, "requires the real TLS PDS harness");
  test.setTimeout(4 * 60_000);
  const server = process.env.SERVER_HTTPS_URL!;
  const pds = process.env.PDS_HTTP_URL!;
  const did = process.env.ACCOUNT_DID!;
  const login = await request.post(
    `${pds}/xrpc/com.atproto.server.createSession`,
    {
      data: {
        identifier: process.env.ACCOUNT_HANDLE,
        password: process.env.ACCOUNT_PASSWORD,
      },
    },
  );
  expect(login.ok()).toBe(true);
  const authorization = {
    Authorization: `Bearer ${(await login.json()).accessJwt}`,
  };
  const collection = "place.stream.bio.page";
  const fixture = {
    $type: collection,
    createdAt: new Date().toISOString(),
    description: {
      plaintext: "Original about",
      facets: [
        {
          index: { byteStart: 0, byteEnd: 8 },
          features: [{ $type: "place.stream.bio.richtextFacet#bold" }],
        },
      ],
      extraDescription: "keep description",
    },
    socials: [
      { platform: "website", url: "https://example.com", handle: "My site" },
    ],
    extraPage: { retained: true },
    layout: {
      $type: "place.stream.bio.layouts.panels",
      extraLayout: "keep layout",
      panels: ["First card", "Second card", "Third card"].map(
        (name, index) => ({
          extraPanel: name,
          blocks: [
            {
              alignment: "left",
              extraEntry: name,
              block: {
                $type: "place.stream.bio.blocks.text",
                plaintext:
                  name + (index === 0 ? "\nMore content\nA taller card" : ""),
                extraBlock: name,
              },
            },
            ...(index === 0
              ? [
                  {
                    block: {
                      $type: "place.stream.bio.blocks.future",
                      payload: { keep: true },
                    },
                  },
                ]
              : []),
          ],
        }),
      ),
    },
  };
  const put = async (record: unknown) => {
    const response = await request.post(
      `${pds}/xrpc/com.atproto.repo.putRecord`,
      {
        headers: authorization,
        data: { repo: did, collection, rkey: "self", record, validate: false },
      },
    );
    expect(response.ok()).toBe(true);
  };
  const read = async () => {
    const response = await request.get(
      `${pds}/xrpc/com.atproto.repo.getRecord?repo=${encodeURIComponent(did)}&collection=${collection}&rkey=self`,
    );
    expect(response.ok()).toBe(true);
    return (await response.json()).value as typeof fixture;
  };
  const save = async (success = true) => {
    const saved = page.waitForResponse((response) =>
      response.url().endsWith("/xrpc/com.atproto.repo.putRecord"),
    );
    await page.getByRole("button", { name: "Save bio", exact: true }).click();
    expect((await saved).ok()).toBe(success);
  };
  await put(fixture);
  try {
    await loginThroughPds(page);
    await page.setViewportSize({ width: 1440, height: 1000 });
    await page.goto(`${server}/dashboard/profile`);
    const about = page.getByRole("textbox", { name: "About you", exact: true });
    await expect(about).toContainText("Original about");
    const cards = page.locator("[data-bio-card]");
    await expect(cards).toHaveCount(3);
    const labels = () =>
      cards.evaluateAll((elements) =>
        elements.map(
          (element) => element.querySelector('[role="textbox"]')?.textContent,
        ),
      );
    const originalOrder = await labels();
    const bounds = () =>
      cards.evaluateAll((elements) =>
        elements.map((element) => {
          const rect = element.getBoundingClientRect();
          return {
            left: rect.left,
            top: rect.top,
            width: rect.width,
            right: rect.right,
            bottom: rect.bottom,
          };
        }),
      );
    await expect
      .poll(
        async () =>
          new Set((await bounds()).map((box) => Math.round(box.left))).size,
      )
      .toBeGreaterThan(1);
    const originalBounds = await bounds();
    const source = cards.nth(0);
    const handle = source.getByRole("button", {
      name: "Drag card 1",
      exact: true,
    });
    const target = cards.nth(2);
    await handle.scrollIntoViewIfNeeded();
    const from = await handle.boundingBox();
    const to = await target.boundingBox();
    expect(from).not.toBeNull();
    expect(to).not.toBeNull();
    const sourceWidth = (await source.boundingBox())!.width;
    const editorCount = await page.getByRole("textbox").count();
    await page.mouse.move(
      from!.x + from!.width / 2,
      from!.y + from!.height / 2,
    );
    await page.mouse.down();
    await page.mouse.move(
      from!.x + from!.width / 2 + 12,
      from!.y + from!.height / 2,
      { steps: 4 },
    );
    const preview = page.locator("[data-bio-drag-preview]");
    await expect(preview).toBeVisible();
    expect((await preview.boundingBox())!.width).toBeCloseTo(sourceWidth, 0);
    expect(await page.getByRole("textbox").count()).toBe(editorCount);
    expect((await bounds()).slice(1)).toEqual(originalBounds.slice(1));
    await page.mouse.move(to!.x + to!.width / 2, to!.y + to!.height / 2, {
      steps: 10,
    });
    await expect(page.locator('[data-bio-drop-target="true"]')).toBeVisible();
    await page.keyboard.press("Escape");
    await page.mouse.up();
    await expect(preview).toHaveCount(0);
    expect(await labels()).toEqual(originalOrder);

    await page.mouse.move(
      from!.x + from!.width / 2,
      from!.y + from!.height / 2,
    );
    await page.mouse.down();
    await page.mouse.move(
      from!.x + from!.width / 2 + 12,
      from!.y + from!.height / 2,
      { steps: 4 },
    );
    await page.mouse.move(1432, 992, { steps: 10 });
    await page.mouse.up();
    await expect(preview).toHaveCount(0);
    expect(await labels()).toEqual(originalOrder);

    await page.mouse.move(
      from!.x + from!.width / 2,
      from!.y + from!.height / 2,
    );
    await page.mouse.down();
    await page.mouse.move(
      from!.x + from!.width / 2 + 12,
      from!.y + from!.height / 2,
      { steps: 4 },
    );
    await page.mouse.move(to!.x + to!.width / 2, to!.y + to!.height / 2, {
      steps: 10,
    });
    await page.mouse.up();
    await expect.poll(labels).not.toEqual(originalOrder);
    await expect(preview).toHaveCount(0);
    const pointerOrder = await labels();

    // Keyboard ordering uses the same persisted array as pointer ordering.
    await handle.focus();
    await page.keyboard.press("Space");
    await expect(preview).toBeVisible();
    await page.keyboard.press("ArrowRight");
    await expect(page.locator('[data-bio-drop-target="true"]')).toBeVisible();
    await page.keyboard.press("Space");
    await expect(preview).toHaveCount(0);
    await expect.poll(labels).not.toEqual(pointerOrder);
    await about.fill("Updated about 😀");
    await save();
    const saved = await read();
    expect(saved.extraPage).toEqual(fixture.extraPage);
    expect(saved.description.extraDescription).toBe("keep description");
    expect(saved.layout.extraLayout).toBe("keep layout");
    expect(saved.layout.panels.map((panel) => panel.extraPanel)).not.toEqual(
      fixture.layout.panels.map((panel) => panel.extraPanel),
    );
    for (const panel of saved.layout.panels) {
      expect(
        "extraEntry" in panel.blocks[0] && panel.blocks[0].extraEntry,
      ).toBe(panel.extraPanel);
      expect(
        "extraBlock" in panel.blocks[0].block &&
          panel.blocks[0].block.extraBlock,
      ).toBe(panel.extraPanel);
    }
    expect(
      saved.layout.panels
        .flatMap((panel) => panel.blocks)
        .find(
          (entry) => entry.block.$type === "place.stream.bio.blocks.future",
        ),
    ).toEqual(fixture.layout.panels[0].blocks[1]);
    expect(Object.keys(saved.layout).sort()).toEqual(
      Object.keys(fixture.layout).sort(),
    );

    await page.emulateMedia({ reducedMotion: "reduce" });
    expect(
      await cards
        .first()
        .evaluate((element) => getComputedStyle(element).transitionProperty),
    ).toBe("none");
    await page.screenshot({
      path: testInfo.outputPath("bio-editor-wide.png"),
      fullPage: true,
    });
    const viewer = await context.newPage();
    try {
      await viewer.addInitScript((node) => {
        localStorage.setItem("streamplaceUrl", node);
        localStorage.setItem("player-transport", "hls");
      }, server);
      await viewer.goto(`${server}/${did}`);
      await expect(viewer.locator("[data-playback-transport]")).toHaveCount(1);
      await expect(viewer.locator("video")).toHaveCount(1);
      const frames = () =>
        viewer
          .locator("video")
          .evaluate(
            (element: HTMLVideoElement) =>
              element.getVideoPlaybackQuality().totalVideoFrames,
          );
      await expect.poll(frames, { timeout: 30_000 }).toBeGreaterThan(0);
      const framesBeforeResize = await frames();
      await expect(
        viewer.getByText("Updated about 😀", { exact: true }),
      ).toBeVisible();
      await viewer.setViewportSize({ width: 390, height: 844 });
      await expect(viewer.locator("[data-playback-transport]")).toHaveCount(1);
      await expect(viewer.locator("video")).toHaveCount(1);
      await expect
        .poll(frames, { timeout: 30_000 })
        .toBeGreaterThan(framesBeforeResize);
      const closeChat = viewer
        .getByRole("button", { name: "Close chat", exact: true })
        .filter({ visible: true });
      if (await closeChat.count()) await closeChat.first().click();
      const publicCards = viewer.getByTestId(/^bio-profile-card-/);
      await expect(publicCards).toHaveCount(3);
      for (let i = 0; i < 3; i++)
        await expect(publicCards.nth(i)).toBeVisible();
      await expect
        .poll(() =>
          publicCards.evaluateAll(
            (elements) =>
              new Set(
                elements.map((element) =>
                  Math.round(element.getBoundingClientRect().left),
                ),
              ).size,
          ),
        )
        .toBe(1);
      await expect(
        viewer.getByRole("link", { name: "My site", exact: true }),
      ).toHaveAttribute("href", "https://example.com/");
      await viewer
        .getByText("Updated about 😀", { exact: true })
        .scrollIntoViewIfNeeded();
      await viewer.screenshot({
        path: testInfo.outputPath("bio-viewer-mobile.png"),
        fullPage: true,
      });
    } finally {
      await viewer.close();
    }

    await put({
      ...saved,
      description: { plaintext: "Changed in another editor" },
    });
    await about.fill("Keep my unsaved draft");
    await save(false);
    await expect(page.getByRole("alert")).toContainText(
      "changed in another editor",
    );
    await expect(about).toContainText("Keep my unsaved draft");
    expect((await read()).description.plaintext).toBe(
      "Changed in another editor",
    );
  } finally {
    const response = await request.post(
      `${pds}/xrpc/com.atproto.repo.deleteRecord`,
      { headers: authorization, data: { repo: did, collection, rkey: "self" } },
    );
    expect(response.ok()).toBe(true);
  }
});
