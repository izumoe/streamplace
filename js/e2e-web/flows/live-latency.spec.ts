import { expect, test } from "@playwright/test";
import { writeFile } from "node:fs/promises";
import { observeAudioOutput } from "./audio-observer";
import { analyzeCadence, analyzePresentation } from "./live-latency-analysis";
import { createLatencyExperiment } from "./live-latency-browser";
import { makeLatencyMarker } from "./live-latency-marker";

// WHIP uses a temporary private stream key, which must not enter traces.
test.use({
  trace: "off",
  video: "off",
  viewport: { width: 1360, height: 900 },
});
test.skip(process.env.E2E_LATENCY !== "1", "opt-in real frame-age measurement");

test("live latency: marked WHIP source to DOM MoQ and native WHEP", async ({
  page,
  browser,
}, testInfo) => {
  test.setTimeout(180_000);
  const server = process.env.SERVER_URL!;
  const did = process.env.ACCOUNT_DID!;
  const key = process.env.E2E_STREAM_KEY!;
  expect(
    Boolean(server && did && key),
    "external-stream harness configuration",
  ).toBe(true);
  const errors: string[] = [];
  const consoleMessages: { type: string; text: string }[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  page.on("console", (message) => {
    if (["warning", "error"].includes(message.type()))
      consoleMessages.push({
        type: message.type(),
        text: message.text().split(key).join("<redacted>"),
      });
  });
  await page.addInitScript((url) => {
    localStorage.setItem("streamplaceUrl", url);
    localStorage.setItem("player-transport", "webrtc");
  }, server);
  await page.addInitScript(observeAudioOutput);
  await page.goto(`${server}/api/healthz`);
  await page.setContent(
    "<!doctype html><title>Live frame-age measurement</title>",
  );
  await page.addScriptTag({
    content: `window.latencyExperiment = (${createLatencyExperiment.toString()})((${makeLatencyMarker.toString()})(), ${JSON.stringify(server)}, ${JSON.stringify(did)});`,
  });
  let failure: string | null = null;
  let result: Awaited<
    ReturnType<ReturnType<typeof createLatencyExperiment>["report"]>
  >;
  try {
    await page.evaluate((key) => window.latencyExperiment.start(key), key);
    await page.evaluate(() => window.latencyExperiment.openViewers());
    const player = page
      .frameLocator("iframe")
      .locator("[data-playback-transport]");
    await expect(player).toHaveAttribute("data-playback-transport", "moq");
    await player.hover();
    await player.locator('button[title="Unmute"]').click();
    await page.evaluate(() => window.latencyExperiment.measure());
  } catch (error) {
    failure = String(error).split(key).join("<redacted>");
  } finally {
    result = await page.evaluate(() => window.latencyExperiment.report());
    await page.evaluate(() => window.latencyExperiment.stop());
  }
  const { steadyStart, steadyEnd, publishStarted } = result.times;
  const sourceCadence = analyzeCadence(
    result.source.generated.map((frame) => frame.at),
    steadyStart ?? NaN,
    steadyEnd ?? NaN,
  );
  const summary = Object.fromEntries(
    (["moq", "whep"] as const).map((transport) => {
      const frames = result[transport];
      const first = frames.find(
        (frame) => frame.latencyMs !== null && frame.latencyMs >= 0,
      );
      const changed = frames.filter((frame) => frame.changed);
      const steady = analyzePresentation(
        changed,
        steadyStart ?? NaN,
        steadyEnd ?? NaN,
      );
      const start =
        transport === "moq"
          ? result.times.moqStarted
          : result.times.whepStarted;
      return [
        transport,
        {
          endpoint:
            transport === "moq"
              ? "parent-rAF observed canvas draw"
              : "native expectedDisplayTime",
          firstMarkerAgeMs: first?.latencyMs ?? null,
          firstMarkerFromPublishMs:
            first && publishStarted !== null
              ? first.display - publishStarted
              : null,
          firstMarkerFromViewerMs:
            first && start !== null ? first.display - start : null,
          observedUniqueFps: steady.fps,
          readbackMaxMs: frames.reduce(
            (max, frame) => Math.max(max, frame.readbackMs),
            0,
          ),
          callbackGapMaxMs: frames.reduce(
            (max, frame) => Math.max(max, frame.callbackGapMs ?? 0),
            0,
          ),
          ...steady,
        },
      ];
    }),
  );
  const artifact = {
    browser: browser.version(),
    binarySha256: process.env.E2E_BINARY_SHA256 ?? null,
    revision: process.env.E2E_REVISION ?? null,
    sourceTransport: "WHIP H264/Opus",
    sourceClock: "publisher parent performance.now()",
    requestedTiming: { settlingMs: 10_000, steadyMs: 20_000 },
    limitations: [
      "canvas generation age, not camera capture age",
      "MoQ observations are not compositor presentation timestamps",
      "main-thread stalls can delay canvas observation; raw callback gaps and readback costs are retained",
    ],
    failure,
    errors,
    consoleMessages,
    sourceCadence,
    summary,
    ...result,
  };
  await writeFile(
    testInfo.outputPath("live-latency.json"),
    JSON.stringify(artifact, null, 2),
  );
  console.log(`live latency: ${JSON.stringify(summary)}`);
  expect(failure).toBeNull();
  expect(errors).toEqual([]);
  expect(result.final.moqTransport).toBe("moq");
  expect(result.received).toEqual({ width: 640, height: 360 });
  expect(result.canvas).toEqual({ width: 640, height: 360 });
  expect(
    result.final.audio.some(
      (output) => output.state === "running" && output.peak > 0.0001,
    ),
  ).toBe(true);
  expect(steadyStart).not.toBeNull();
  expect(steadyEnd! - steadyStart!).toBe(20_000);
  // RTCStats uses timeOrigin + now(); round outwards for timestamp precision.
  for (const snapshot of result.rtc) {
    const capturedAt = Math.ceil(result.parentTimeOrigin + snapshot.at);
    for (const report of [...snapshot.publisher, ...snapshot.whep]) {
      expect(
        Math.floor(Number(report.timestamp)),
        "native RTC report precedes the speaker sample capture",
      ).toBeLessThanOrEqual(capturedAt);
    }
  }
  // These bounds reject an unhealthy experiment or mostly frozen playback.
  // They leave headroom around the measured 30 fps source and 28–30 fps viewers.
  expect(
    sourceCadence.fps,
    "healthy marked source cadence",
  ).toBeGreaterThanOrEqual(25);
  expect(
    sourceCadence.maxGapMs,
    "source generation continuity",
  ).toBeLessThanOrEqual(500);
  const audioSnapshots = result.rtc.filter(
    (snapshot) => snapshot.at >= steadyStart! && snapshot.at < steadyEnd!,
  );
  expect(
    audioSnapshots.length,
    "regular speaker observations",
  ).toBeGreaterThanOrEqual(18);
  for (const snapshot of audioSnapshots) {
    expect(
      snapshot.audio.some(
        (output) => output.state === "running" && output.peak > 0.0001,
      ),
      `real speaker PCM at ${snapshot.at}`,
    ).toBe(true);
  }
  const audioPositions = result.audioBuffers.flatMap((buffer) =>
    buffer.positions.filter(
      (position) => position.at >= steadyStart! && position.at < steadyEnd!,
    ),
  );
  expect(
    audioPositions.length,
    "regular native audio cursor reports",
  ).toBeGreaterThanOrEqual(((steadyEnd! - steadyStart!) / 1000) * 50);
  expect(
    audioPositions.filter((position) => position.stalled),
    "the native speaker buffer remains playing throughout steady observation",
  ).toEqual([]);
  for (const buffer of result.audioBuffers) {
    const positions = buffer.positions.filter(
      (position) => position.at >= steadyStart! && position.at < steadyEnd!,
    );
    expect(
      positions.every(
        (position, index) =>
          index === 0 || position.timestamp > positions[index - 1].timestamp,
      ),
      "the native speaker cursor advances at every report",
    ).toBe(true);
  }
  for (const [reports, direction] of [
    [result.final.publisher, "outbound-rtp"],
    [result.final.whep, "inbound-rtp"],
  ] as const) {
    for (const [kind, mimeType] of [
      ["video", "video/h264"],
      ["audio", "audio/opus"],
    ]) {
      const media = reports.find(
        (report) => report.type === direction && report.kind === kind,
      );
      expect(media, `${direction}: actual ${kind} media`).toBeDefined();
      const codec = reports.find((report) => report.id === media!.codecId);
      expect(String(codec?.mimeType).toLowerCase()).toBe(mimeType);
      expect(
        Number(
          media![direction === "outbound-rtp" ? "bytesSent" : "bytesReceived"],
        ),
      ).toBeGreaterThan(0);
    }
  }
  for (const transport of ["moq", "whep"]) {
    expect(
      summary[transport].fps,
      `${transport}: continuing new marked frames`,
    ).toBeGreaterThanOrEqual(20);
    expect(
      summary[transport].maxGapMs,
      `${transport}: no long freeze`,
    ).toBeLessThanOrEqual(1000);
    expect(summary[transport].firstMarkerAgeMs).not.toBeNull();
  }
});
