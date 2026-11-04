import { readFile, writeFile } from "node:fs/promises";
import { resolve } from "node:path";
import { loginThroughPds } from "../../e2e-web/flows/login";
import { expect, test } from "./fixtures";

test.use({ trace: "off", video: "off" });

test("uploaded bio images load from the PDS when the image CDN is unavailable", async ({
  page,
  context,
  request,
}, testInfo) => {
  test.skip(!process.env.SERVER_HTTPS_URL, "requires the real TLS PDS harness");
  test.setTimeout(3 * 60_000);
  const server = process.env.SERVER_HTTPS_URL!;
  const pds = process.env.PDS_HTTP_URL!;
  const did = process.env.ACCOUNT_DID!;
  const collection = "place.stream.bio.page";
  const session = await request.post(
    `${pds}/xrpc/com.atproto.server.createSession`,
    {
      data: {
        identifier: process.env.ACCOUNT_HANDLE,
        password: process.env.ACCOUNT_PASSWORD,
      },
    },
  );
  expect(session.ok()).toBe(true);
  const headers = {
    Authorization: `Bearer ${(await session.json()).accessJwt}`,
  };
  const remove = async () => {
    const response = await request.post(
      `${pds}/xrpc/com.atproto.repo.deleteRecord`,
      {
        headers,
        data: { repo: did, collection, rkey: "self" },
      },
    );
    expect(response.ok()).toBe(true);
  };
  await remove();
  try {
    await loginThroughPds(page);
    await page.goto(`${server}/dashboard/profile`);
    const about = page.getByRole("textbox", { name: "About you", exact: true });
    await expect(about).toBeVisible();
    await about.fill("A profile with a PDS image");
    await page
      .getByRole("button", {
        name: "Add content or move selected block",
        exact: true,
      })
      .click();
    await page.getByRole("menuitem", { name: "Image", exact: true }).click();
    await page
      .getByLabel("Add image file", { exact: true })
      .setInputFiles(
        resolve(testInfo.project.testDir, "../../app/assets/images/jelly.png"),
      );
    await page
      .getByLabel("Image description", { exact: true })
      .fill("Uploaded jelly");
    await page
      .getByRole("button", { name: "Insert image", exact: true })
      .click();
    const preview = page.getByRole("img", {
      name: "Uploaded jelly",
      exact: true,
    });
    await expect(preview).toBeVisible();
    await expect
      .poll(() =>
        preview.evaluate((image: HTMLImageElement) => image.naturalWidth),
      )
      .toBeGreaterThan(0);
    await page.getByRole("button", { name: "Save bio", exact: true }).click();
    await expect(
      page.getByRole("status").filter({ hasText: "Bio saved" }),
    ).toBeVisible();
    const record = await request.get(
      `${pds}/xrpc/com.atproto.repo.getRecord?${new URLSearchParams({ repo: did, collection, rkey: "self" })}`,
    );
    expect(record.ok()).toBe(true);
    const body = await record.json();
    const image = body.value.layout.panels
      .flatMap(
        (panel: {
          blocks: {
            block: { $type: string; image?: { ref: { $link: string } } };
          }[];
        }) => panel.blocks,
      )
      .find(
        (entry: { block: { $type: string } }) =>
          entry.block.$type === "place.stream.bio.blocks.image",
      );
    const cid = image.block.image.ref.$link as string;
    expect(cid).toBeTruthy();
    const fixture = await readFile(
      resolve(testInfo.project.testDir, "../../app/assets/images/jelly.png"),
    );
    expect(image.block.image.size).toBe(fixture.byteLength);
    const viewer = await context.newPage();
    try {
      // Keep the synthetic account private while exercising a real CDN failure.
      await viewer.route(`https://cdn.bsky.app/**/${cid}@jpeg`, (route) =>
        route.abort("failed"),
      );
      await viewer.addInitScript(
        (url) => localStorage.setItem("streamplaceUrl", url),
        server,
      );
      const blob = viewer.waitForResponse((response) => {
        const url = new URL(response.url());
        return (
          response.request().method() === "GET" &&
          url.pathname === "/xrpc/com.atproto.sync.getBlob" &&
          url.searchParams.get("cid") === cid
        );
      });
      await viewer.goto(`${server}/${did}`);
      const loaded = viewer.getByRole("img", {
        name: "Uploaded jelly",
        exact: true,
      });
      const card = viewer.getByTestId("bio-profile-card-0");
      await expect(card).toBeVisible();
      await card.scrollIntoViewIfNeeded();
      await expect(loaded).toHaveAttribute("src", /^blob:/);
      const response = await blob;
      expect(response.status()).toBe(200);
      const blobURL = new URL(response.url());
      expect(blobURL.origin).toBe(new URL(process.env.PDS_HTTPS_URL!).origin);
      expect(blobURL.searchParams.get("did")).toBe(did);
      const direct = await request.get(
        `${pds}${blobURL.pathname}${blobURL.search}`,
      );
      expect(direct.status()).toBe(200);
      expect(await direct.body()).toEqual(fixture);
      const browserFetch = await viewer.evaluate(async (url) => {
        const fetched = await fetch(url);
        return {
          status: fetched.status,
          bytes: (await fetched.arrayBuffer()).byteLength,
        };
      }, blobURL.toString());
      expect(browserFetch.status).toBe(200);
      expect(browserFetch.bytes).toBe(fixture.byteLength);
      await expect
        .poll(() =>
          loaded.evaluate((element: HTMLImageElement) => element.naturalWidth),
        )
        .toBeGreaterThan(0);
      await expect(loaded).toHaveAttribute("src", /^blob:/);
      const decoded = await loaded.evaluate((element: HTMLImageElement) => ({
        width: element.naturalWidth,
        height: element.naturalHeight,
      }));
      const proof = testInfo.outputPath("bio-image-verification.json");
      await writeFile(
        proof,
        JSON.stringify(
          {
            fixtureBytes: fixture.byteLength,
            browserBytes: browserFetch.bytes,
            decoded,
          },
          null,
          2,
        ),
      );
      await testInfo.attach("bio-image-verification", {
        path: proof,
        contentType: "application/json",
      });
    } finally {
      await viewer.close();
    }
  } finally {
    await remove();
  }
});
