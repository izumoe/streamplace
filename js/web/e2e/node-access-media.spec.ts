import { writeFile } from "node:fs/promises";
import { loginThroughPds } from "../../e2e-web/flows/login";
import { expect, test } from "./fixtures";

test.use({ trace: "off", video: "off" });

test("publishing approval controls live ingest independently of peer carriage", async ({
  page,
  context,
  request,
}, testInfo) => {
  test.skip(
    process.env.E2E_UNAPPROVED_STREAM !== "1",
    "requires the unapproved-stream TLS PDS harness",
  );
  test.setTimeout(5 * 60_000);
  const server = process.env.SERVER_URL!;
  const peer = process.env.SERVER2_URL!;
  const httpsServer = process.env.SERVER_HTTPS_URL!;
  const internal = process.env.SERVER_INTERNAL_URL!;
  const pds = process.env.PDS_HTTP_URL!;
  const did = process.env.ACCOUNT_DID!;
  const key = process.env.E2E_STREAM_KEY!;
  expect(Boolean(server && peer && httpsServer && pds && did)).toBe(true);

  async function latest(node: string): Promise<string | null> {
    const response = await request.get(
      `${node}/xrpc/place.stream.live.getSegments?userDID=${encodeURIComponent(did)}&limit=1`,
    );
    expect(response.ok()).toBe(true);
    const body = (await response.json()) as {
      segments?: { record: { id: string; creator: string } }[];
    };
    const segments = body.segments ?? [];
    for (const segment of segments) expect(segment.record.creator).toBe(did);
    return segments[0]?.record.id ?? null;
  }
  async function workerStarts(): Promise<number> {
    const response = await request.get(`${internal}/metrics`);
    expect(response.ok()).toBe(true);
    const value = (await response.text()).match(
      /^streamplace_ingest_worker_starts_total\{transport="whip"\} ([\d.e+-]+)$/m,
    );
    return value ? Number(value[1]) : 0;
  }
  async function deniedWHIP(node: string) {
    const response = await request.post(`${node}/api/ingest/webrtc`, {
      headers: {
        Authorization: `Bearer ${key}`,
        "Content-Type": "application/sdp",
      },
      data: "v=0\r\n",
    });
    // A registered key must be rejected before the intentionally invalid SDP.
    expect(response.status()).toBe(401);
  }
  async function storedKeys(): Promise<{ uri: string; cid: string }[]> {
    const response = await request.get(
      `${pds}/xrpc/com.atproto.repo.listRecords?repo=${encodeURIComponent(did)}&collection=place.stream.key`,
    );
    expect(response.ok()).toBe(true);
    const body = (await response.json()) as {
      records: { uri: string; cid: string }[];
    };
    return body.records
      .map(({ uri, cid }) => ({ uri, cid }))
      .sort((a, b) => a.uri.localeCompare(b.uri));
  }
  async function advancing(node: string, previous: string | null) {
    await expect
      .poll(
        async () => {
          const current = await latest(node);
          return current !== null && current !== previous;
        },
        { timeout: 60_000, intervals: [500] },
      )
      .toBe(true);
    const first = await latest(node);
    expect(first).not.toBeNull();
    await expect
      .poll(
        async () => {
          const current = await latest(node);
          return current !== null && current !== first;
        },
        { timeout: 30_000, intervals: [500] },
      )
      .toBe(true);
  }
  async function peerPlayback() {
    const viewer = await context.newPage();
    try {
      await viewer.addInitScript((node) => {
        localStorage.setItem("streamplaceUrl", node);
        localStorage.setItem("player-transport", "hls");
      }, peer);
      const [, playlist, segment] = await Promise.all([
        viewer.goto(`${peer}/${did}`),
        viewer.waitForResponse((response) => {
          const url = new URL(response.url());
          return (
            url.origin === new URL(peer).origin &&
            url.pathname === "/xrpc/place.stream.playback.getLivePlaylist" &&
            response.ok()
          );
        }),
        viewer.waitForResponse((response) => {
          const url = new URL(response.url());
          return (
            url.origin === new URL(peer).origin &&
            url.pathname === "/xrpc/place.stream.playback.getLiveSegment" &&
            /^\d+(?:\.m4s)?$/.test(url.searchParams.get("seg") ?? "") &&
            response.ok()
          );
        }),
      ]);
      expect(playlist.ok()).toBe(true);
      const mediaBytes = (await segment.body()).byteLength;
      expect(mediaBytes).toBeGreaterThan(0);
      const video = viewer.locator("video").first();
      await expect(video).toBeVisible();
      const frames = () =>
        video.evaluate(
          (element: HTMLVideoElement) =>
            element.getVideoPlaybackQuality().totalVideoFrames,
        );
      await expect.poll(frames, { timeout: 30_000 }).toBeGreaterThan(0);
      return { mediaBytes, decodedFrames: await frames() };
    } finally {
      await viewer.close();
    }
  }
  async function save(effect: "allow" | "deny"): Promise<string> {
    await page.getByLabel("Account DID", { exact: true }).fill(did);
    await page
      .getByRole("combobox", { name: "Capability", exact: true })
      .selectOption("streamer");
    await page
      .getByRole("combobox", { name: "Action", exact: true })
      .selectOption(effect);
    const saved = page.waitForResponse((response) =>
      response.url().endsWith("/xrpc/place.stream.access.createGrant"),
    );
    await page
      .getByRole("button", { name: "Save access entry", exact: true })
      .click();
    const response = await saved;
    expect(response.ok()).toBe(true);
    const grant = (await response.json()) as { id: string };
    await expect(page.locator(`[data-entry-id="${grant.id}"]`)).toContainText(
      effect === "allow" ? "Publishing permitted" : "Publishing denied",
    );
    return grant.id;
  }
  async function remove(id: string, effect: "allow" | "deny") {
    const row = page.locator(`[data-entry-id="${id}"]`);
    const removed = page.waitForResponse((response) =>
      response.url().endsWith("/xrpc/place.stream.access.deleteGrant"),
    );
    await row
      .getByRole("button", {
        name: effect === "allow" ? "Revoke approval" : "Remove denial",
        exact: true,
      })
      .click();
    expect((await removed).ok()).toBe(true);
    await expect(row).toHaveCount(0);
  }

  await loginThroughPds(page);
  const statusResponse = page.waitForResponse((response) =>
    response.url().includes("/xrpc/place.stream.access.getStatus"),
  );
  await page.goto(`${httpsServer}/settings/access`);
  await expect(
    page.getByRole("heading", { name: "Node access", exact: true }),
  ).toBeVisible();
  const status = (await (await statusResponse).json()) as {
    subject: string;
    capabilities: { capability: string; allowed: boolean }[];
  };
  expect(status.subject).toBe(did);
  expect(
    status.capabilities.find((entry) => entry.capability === "admin")?.allowed,
  ).toBe(true);
  expect(
    status.capabilities.find((entry) => entry.capability === "streamer")
      ?.allowed,
  ).toBe(false);
  expect(Boolean(internal && key)).toBe(true);
  await deniedWHIP(server);
  expect(await latest(server)).toBeNull();
  const keysBefore = await storedKeys();
  expect(keysBefore.length).toBeGreaterThan(0);
  const startsBefore = await workerStarts();
  let allowance: string | undefined;
  let denial: string | undefined;
  try {
    allowance = await save("allow");
    await expect
      .poll(workerStarts, { timeout: 30_000 })
      .toBeGreaterThan(startsBefore);
    await advancing(server, null);
    await advancing(peer, null);
    await deniedWHIP(peer);
    const approvedPlayback = await peerPlayback();
    const approved = { origin: await latest(server), peer: await latest(peer) };

    denial = await save("deny");
    await deniedWHIP(server);
    let stopped = { origin: await latest(server), peer: await latest(peer) };
    let unchanged = 0;
    await expect
      .poll(
        async () => {
          const [origin, peerTip] = await Promise.all([
            latest(server),
            latest(peer),
          ]);
          unchanged =
            origin === stopped.origin && peerTip === stopped.peer
              ? unchanged + 1
              : 0;
          stopped = { origin, peer: peerTip };
          return unchanged;
        },
        { timeout: 20_000, intervals: [1000] },
      )
      .toBeGreaterThanOrEqual(3);
    expect(stopped.origin).not.toBeNull();
    expect(stopped.peer).not.toBeNull();
    const deniedStarts = await workerStarts();
    const deniedAt = Date.now();
    const until = deniedAt + 5000;
    let probes = 0;
    while (Date.now() < until) {
      await deniedWHIP(server);
      probes++;
      expect(await latest(server)).toBe(stopped.origin);
      expect(await latest(peer)).toBe(stopped.peer);
      expect(await workerStarts()).toBe(deniedStarts);
      await new Promise((resolve) => setTimeout(resolve, 500));
    }
    const deniedObservedMs = Date.now() - deniedAt;
    expect(probes).toBeGreaterThanOrEqual(2);
    const anonymous = await request.get(
      `${server}/xrpc/place.stream.access.getStatus`,
    );
    expect(anonymous.ok()).toBe(true);
    const publicStatus = (await anonymous.json()) as {
      capabilities: { capability: string; allowed: boolean }[];
    };
    expect(
      publicStatus.capabilities.find((entry) => entry.capability === "viewer")
        ?.allowed,
    ).toBe(true);
    expect(await storedKeys()).toEqual(keysBefore);

    await remove(denial, "deny");
    denial = undefined;
    await expect
      .poll(workerStarts, { timeout: 30_000 })
      .toBeGreaterThan(deniedStarts);
    await advancing(server, stopped.origin);
    await advancing(peer, stopped.peer);
    const resumedPlayback = await peerPlayback();
    const resumed = { origin: await latest(server), peer: await latest(peer) };
    const resumedStarts = await workerStarts();
    const keysAfter = await storedKeys();
    expect(keysAfter).toEqual(keysBefore);
    const evidence = testInfo.outputPath("node-access-media-acceptance.json");
    await writeFile(
      evidence,
      JSON.stringify(
        {
          did,
          approved,
          stopped,
          resumed,
          workerStarts: {
            before: startsBefore,
            denied: deniedStarts,
            resumed: resumedStarts,
          },
          denied: { observedMs: deniedObservedMs, probes },
          peerPlayback: {
            approved: approvedPlayback,
            resumed: resumedPlayback,
          },
          streamKeys: { before: keysBefore, after: keysAfter },
        },
        null,
        2,
      ),
    );
    await testInfo.attach("node-access-media-acceptance.json", {
      path: evidence,
      contentType: "application/json",
    });
  } finally {
    if (denial) await remove(denial, "deny");
    if (allowance) await remove(allowance, "allow");
  }
});
