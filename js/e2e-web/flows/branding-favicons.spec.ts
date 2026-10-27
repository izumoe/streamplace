import {
  expect,
  test,
  type APIRequestContext,
  type Page,
} from "@playwright/test";
import { loginThroughPds } from "./login";

// The DOM editor uploads the generic icon. Color-scheme variants use the real
// admin API; both routes must retain MIME types, ICO conversion and fallbacks.
//
// The harness account is the node's branding admin (SP_ADMIN_DIDS in
// pkg/cmd/e2e.go); uploads go through the app's own file chooser and the
// node's authenticated XRPC, so this needs the harness's HTTPS mode.
const HTTPS_URL = process.env.SERVER_HTTPS_URL;
const SERVER_URL = process.env.SERVER_URL!;

test.skip(!HTTPS_URL, "harness started without its HTTPS hostnames");

const ICONS: Record<string, string> = {
  favicon: `<svg xmlns="http://www.w3.org/2000/svg" width="32" height="32"><rect width="32" height="32" fill="#808080"/></svg>`,
  faviconLight: `<svg xmlns="http://www.w3.org/2000/svg" width="32" height="32"><circle cx="16" cy="16" r="12" fill="#ffd000"/></svg>`,
  faviconDark: `<svg xmlns="http://www.w3.org/2000/svg" width="32" height="32"><circle cx="16" cy="16" r="12" fill="#2040ff"/></svg>`,
};

type Icon = { type: string; body: string };
const icon = (key: string): Icon => ({
  type: "image/svg+xml",
  body: ICONS[key],
});

const xrpc = (method: string) => (response: { url(): string }) =>
  new URL(response.url()).pathname === `/xrpc/place.stream.branding.${method}`;

// The icon a browser shows for the current color scheme: the last
// <link rel="icon"> in <head> whose media applies (Chromium and Firefox both
// prefer the last one declared), read from the hydrated page or from its HTML
// as the node serves it. SVG bodies come back as text, anything else base64;
// a link typed differently from what its href serves reports both types, and
// a failed read reports its error, so a poll retries it.
async function shownIcon(page: Page, source: "dom" | "html"): Promise<Icon> {
  const read = page.evaluate(async (source) => {
    let head = document.head;
    if (source === "html") {
      const html = await fetch(location.href, { cache: "no-store" });
      head = new DOMParser().parseFromString(
        await html.text(),
        "text/html",
      ).head;
    }
    const links = Array.from(
      head.querySelectorAll<HTMLLinkElement>(':scope > link[rel~="icon"]'),
    ).filter((link) => !link.media || matchMedia(link.media).matches);
    const link = links[links.length - 1];
    if (!link) return { type: "no applicable icon link", body: "" };
    const href = new URL(link.getAttribute("href")!, location.href).href;
    // Hydrated links use the browser's normal cache. Static HTML checks ask
    // the server directly, since its public icon routes cache for five minutes.
    const res = await fetch(
      href,
      source === "html" ? { cache: "no-store" } : undefined,
    );
    const served = res.headers.get("content-type")?.split(";")[0] ?? "";
    const type =
      link.type && link.type !== served
        ? `${link.type} (served ${served})`
        : served;
    if (served === "image/svg+xml") return { type, body: await res.text() };
    let binary = "";
    for (const byte of new Uint8Array(await res.arrayBuffer())) {
      binary += String.fromCharCode(byte);
    }
    return { type, body: btoa(binary) };
  }, source);
  return read.catch((e: Error) => ({ type: `error: ${e.message}`, body: "" }));
}

async function expectShown(
  page: Page,
  scheme: "light" | "dark",
  expected: Icon,
  source: "dom" | "html" = "dom",
) {
  await page.emulateMedia({ colorScheme: scheme });
  await expect
    .poll(() => shownIcon(page, source), {
      message: `${scheme} ${source} favicon`,
    })
    .toEqual(expected);
}

// Decode the actual ICO as a browser image, not just a renamed SVG/PNG.
async function readICO(page: Page, pixel?: number[]): Promise<string> {
  const result = await page.evaluate(async () => {
    const response = await fetch("/favicon.ico", { cache: "no-store" });
    const blob = await response.blob();
    const bytes = new Uint8Array(await blob.arrayBuffer());
    const url = URL.createObjectURL(blob);
    try {
      const image = new Image();
      image.src = url;
      await image.decode();
      const canvas = document.createElement("canvas");
      canvas.width = image.naturalWidth;
      canvas.height = image.naturalHeight;
      const ctx = canvas.getContext("2d")!;
      ctx.drawImage(image, 0, 0);
      let binary = "";
      for (const byte of bytes) binary += String.fromCharCode(byte);
      return {
        status: response.status,
        type: response.headers.get("content-type"),
        header: Array.from(bytes.slice(0, 4)),
        pixel: Array.from(
          ctx.getImageData(canvas.width >> 1, canvas.height >> 1, 1, 1).data,
        ),
        body: btoa(binary),
      };
    } finally {
      URL.revokeObjectURL(url);
    }
  });
  expect(result.status).toBe(200);
  expect(result.type).toBe("image/x-icon");
  expect(result.header).toEqual([0, 0, 1, 0]);
  if (pixel) expect(result.pixel).toEqual(pixel);
  return result.body;
}

async function updateIcon(
  request: APIRequestContext,
  key: string,
  remove = false,
) {
  const pds = process.env.PDS_HTTP_URL!;
  const session = await request.post(
    `${pds}/xrpc/com.atproto.server.createSession`,
    {
      data: {
        identifier: process.env.ACCOUNT_DID!,
        password: process.env.ACCOUNT_PASSWORD!,
      },
    },
  );
  expect(session.ok()).toBe(true);
  const { accessJwt } = await session.json();
  const document = await request.get(`${SERVER_URL}/.well-known/did.json`);
  expect(document.ok()).toBe(true);
  const { id } = await document.json();
  const method = `place.stream.branding.${remove ? "deleteBlob" : "updateBlob"}`;
  const auth = await request.get(
    `${pds}/xrpc/com.atproto.server.getServiceAuth`,
    {
      params: { aud: id, lxm: method },
      headers: { Authorization: `Bearer ${accessJwt}` },
    },
  );
  expect(auth.ok()).toBe(true);
  const { token } = await auth.json();
  const response = await request.post(`${SERVER_URL}/xrpc/${method}`, {
    headers: { Authorization: `Bearer ${token}` },
    data: remove
      ? { key }
      : {
          key,
          data: Buffer.from(ICONS[key]).toString("base64"),
          mimeType: "image/svg+xml",
        },
  });
  expect(remove ? [200, 404] : [200]).toContain(response.status());
}

async function uploadIcon(page: Page, request: APIRequestContext, key: string) {
  if (key !== "favicon") {
    await updateIcon(request, key);
    return;
  }
  await page.goto(`${HTTPS_URL}/settings/branding`);
  const updated = page.waitForResponse(xrpc("updateBlob"));
  const chooser = page.waitForEvent("filechooser");
  await page
    .getByText("Default favicon", { exact: true })
    .locator("..")
    .getByRole("button", { name: "Upload", exact: true })
    .click();
  await (
    await chooser
  ).setFiles({
    name: `${key}.svg`,
    mimeType: "image/svg+xml",
    buffer: Buffer.from(ICONS[key]),
  });
  expect((await updated).ok()).toBe(true);
}

test.afterEach(async ({ request }) => {
  for (const key of Object.keys(ICONS)) await updateIcon(request, key, true);
});

test("branding-favicons: light and dark favicons follow the color scheme", async ({
  page,
  request,
}) => {
  // OAuth login plus several uploads and reloads outlast the default 90s.
  test.setTimeout(4 * 60_000);

  await loginThroughPds(page);
  for (const key of Object.keys(ICONS)) await updateIcon(request, key, true);
  await page.goto(`${HTTPS_URL}/`);
  // With no favicon branded, the tab shows the app's bundled icon.
  await page.emulateMedia({ colorScheme: "light" });
  expect((await shownIcon(page, "dom")).type).toMatch(/^image\//);
  const bundled = await shownIcon(page, "dom");
  const bundledICO = await readICO(page);

  // The background-backed default works in either scheme on its own.
  await uploadIcon(page, request, "favicon");
  await page.goto(`${HTTPS_URL}/`);
  await expectShown(page, "light", icon("favicon"));
  await expectShown(page, "dark", icon("favicon"));
  await readICO(page, [128, 128, 128, 255]);

  // Transparent variants override it independently without altering the files.
  for (const key of ["faviconLight", "faviconDark"])
    await uploadIcon(page, request, key);
  await page.reload();

  // Each scheme gets its own icon, live: no reload between the switches.
  await expectShown(page, "light", icon("faviconLight"));
  await expectShown(page, "dark", icon("faviconDark"));
  // Theme variants must not replace the generic legacy-client ICO.
  await readICO(page, [128, 128, 128, 255]);

  // An anonymous live-stream page gets the same branding before hydration.
  const publicStream = await request.get(
    `${SERVER_URL}/${process.env.ACCOUNT_DID}`,
  );
  expect(publicStream.ok()).toBe(true);
  const html = await publicStream.text();
  for (const body of Object.values(ICONS)) {
    expect(html).toContain(
      `href="data:image/svg+xml;base64,${Buffer.from(body).toString("base64")}"`,
    );
  }
  await page.goto(`${HTTPS_URL}/${process.env.ACCOUNT_DID}`);
  await readICO(page, [128, 128, 128, 255]);
  for (const source of ["dom", "html"] as const) {
    await expectShown(page, "dark", icon("faviconDark"), source);
    await expectShown(page, "light", icon("faviconLight"), source);
  }

  // Prime the same HTTP cache used by the initial HTML's favicon links.
  // Deleting uploads must not bring these cached images back into the tab.
  await page.evaluate(async () => {
    await fetch("/favicon.png");
    await fetch("/favicon.png?scheme=dark");
  });

  // Without its own variant, a scheme falls back to the generic favicon...
  await updateIcon(request, "faviconDark", true);
  await page.reload();
  await expectShown(page, "dark", icon("favicon"));
  await expectShown(page, "dark", icon("favicon"), "html");
  await expectShown(page, "light", icon("faviconLight"));

  // ...and without that, to the bundled one.
  await page.goto(`${HTTPS_URL}/settings/branding`);
  const deleted = page.waitForResponse(xrpc("deleteBlob"));
  await page
    .getByText("Default favicon", { exact: true })
    .locator("..")
    .getByRole("button", { name: "Delete", exact: true })
    .click();
  expect((await deleted).ok()).toBe(true);
  await page.goto(`${HTTPS_URL}/${process.env.ACCOUNT_DID}`);
  expect(await readICO(page)).toBe(bundledICO);
  await expectShown(page, "dark", bundled);
  await expectShown(page, "light", icon("faviconLight"));
});
