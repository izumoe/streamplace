import {
  expect,
  test,
  type Locator,
  type Page,
  type TestInfo,
} from "@playwright/test";

import { observeAudioOutput } from "../../e2e-web/flows/audio-observer";

declare global {
  interface Window {
    webrtcStatsTest?: {
      paused: boolean;
      peers: Map<RTCPeerConnection, number>;
    };
    loseMoqConnection?: () => Promise<void>;
    moqCodecSupport?: { codec: string; supported: boolean }[];
    moqVideoPressure?: {
      longTasks: { at: number; duration: number }[];
      taskLag: { at: number; lag: number }[];
    };
    moqVideoDecoders?: {
      decoder: VideoDecoder;
      maxPending: number;
      queues: { at: number; pending: number }[];
      outputs: number;
      errors: string[];
    }[];
  }
}

test.setTimeout(90_000);

test.beforeEach(async ({ page }) => {
  const server = process.env.SERVER_URL;
  if (!server || !process.env.ACCOUNT_DID) {
    throw new Error("SERVER_URL and ACCOUNT_DID require the local e2e harness");
  }
  await page.addInitScript((url) => {
    localStorage.setItem("streamplaceUrl", url);
    localStorage.setItem("player-transport", "webrtc");
  }, server);
});

async function openLivePlayer(page: Page) {
  await page.goto(`/embed/${encodeURIComponent(process.env.ACCOUNT_DID!)}/`);
  return page.locator("[data-playback-transport]");
}

async function expectVideoAdvancing(video: Locator) {
  const frames = () =>
    video.evaluate(
      (element: HTMLVideoElement) =>
        element.getVideoPlaybackQuality().totalVideoFrames,
    );
  const initial = await frames();
  await expect.poll(frames).toBeGreaterThan(initial);
}

async function expectAudioPlaying(page: Page, testInfo: TestInfo) {
  try {
    await expect
      .poll(() =>
        page.evaluate(() => {
          return (
            window.moqAudioAnalysers?.some((analyser) => {
              const samples = new Float32Array(analyser.fftSize);
              analyser.getFloatTimeDomainData(samples);
              return (
                analyser.context.state === "running" &&
                samples.some((sample) => Math.abs(sample) > 0.0001)
              );
            }) ?? false
          );
        }),
      )
      .toBe(true);
  } catch (error) {
    const audio = await page.evaluate(() => ({
      output: window.moqAudioAnalysers?.map((analyser) => {
        const samples = new Float32Array(analyser.fftSize);
        analyser.getFloatTimeDomainData(samples);
        return {
          state: analyser.context.state,
          currentTime: analyser.context.currentTime,
          sampleRate: analyser.context.sampleRate,
          peak: samples.reduce(
            (peak, sample) => Math.max(peak, Math.abs(sample)),
            0,
          ),
        };
      }),
    }));
    await testInfo.attach("native-audio-output", {
      body: JSON.stringify(audio),
      contentType: "application/json",
    });
    throw error;
  }
}

test("Enhanced RTMP keeps same-height H264 tracks distinct and decodes supported AV1", async ({
  page,
}, testInfo) => {
  test.skip(
    process.env.E2E_ERTMP !== "1",
    "requires the real multitrack publisher",
  );
  const pageErrors: string[] = [];
  page.on("pageerror", (error) => pageErrors.push(error.message));
  await page.addInitScript(observeAudioOutput);
  await page.addInitScript(() => {
    window.moqCodecSupport = [];
    const supports = VideoDecoder.isConfigSupported;
    VideoDecoder.isConfigSupported = async function (config) {
      const result = await Reflect.apply(supports, this, [config]);
      window.moqCodecSupport!.push({
        codec: config.codec,
        supported: result.supported === true,
      });
      return result;
    };
  });
  const player = await openLivePlayer(page);
  await expect(player).toHaveAttribute("data-playback-transport", "moq");
  const canvas = player.locator("canvas");
  await expect(canvas).toHaveCSS("opacity", "1");
  await player.hover();
  await player.locator('button[title="Unmute"]').click();
  await expectAudioPlaying(page, testInfo);

  const picture = (surface: Locator) =>
    surface.evaluate((element: HTMLCanvasElement | HTMLVideoElement) => {
      const copy = document.createElement("canvas");
      copy.width = 160;
      copy.height = 120;
      const context = copy.getContext("2d", { willReadFrequently: true })!;
      context.drawImage(element, 0, 0, copy.width, copy.height);
      const [red, green, blue] = context.getImageData(8, 8, 1, 1).data;
      return {
        color:
          red > green + 60 && red > blue + 60
            ? "red"
            : blue > red + 60 && blue > green + 60
              ? "blue"
              : green > red + 60 && green > blue + 60
                ? "green"
                : "unknown",
        frame: copy.toDataURL(),
      };
    });
  async function selectAndSee(surface: Locator, label: string, color: string) {
    await player.hover();
    await player.locator('button[title="Settings"]').click();
    await page.getByRole("menuitemradio", { name: label, exact: true }).click();
    await page.keyboard.press("Escape");
    await expect.poll(async () => (await picture(surface)).color).toBe(color);
    const frame = (await picture(surface)).frame;
    await expect
      .poll(async () => (await picture(surface)).frame)
      .not.toBe(frame);
  }

  await selectAndSee(canvas, "120p (H.264, video1)", "red");
  await selectAndSee(canvas, "120p (H.264, video2)", "blue");
  await expect
    .poll(() =>
      page.evaluate(() =>
        window.moqCodecSupport!.find(({ codec }) => codec.startsWith("av01.")),
      ),
    )
    .not.toBeUndefined();
  const support = await page.evaluate(() => window.moqCodecSupport!);
  await testInfo.attach("native-video-codec-support", {
    body: JSON.stringify(support),
    contentType: "application/json",
  });
  const av1 = support.find(({ codec }) => codec.startsWith("av01."))!;
  testInfo.annotations.push({
    type: "AV1",
    description: av1.supported
      ? "native decode supported"
      : "native decode unsupported",
  });
  if (av1.supported) {
    await selectAndSee(canvas, "120p (AV1, video3)", "green");
    await expectAudioPlaying(page, testInfo);
  } else {
    await player.hover();
    await player.locator('button[title="Settings"]').click();
    await expect(page.getByRole("menuitemradio", { name: /AV1/ })).toHaveCount(
      0,
    );
    await page.keyboard.press("Escape");
  }

  await player.hover();
  await player.locator('button[title="Settings"]').click();
  await page
    .getByRole("menuitemradio", { name: "Standard", exact: true })
    .click();
  await page.keyboard.press("Escape");
  await expect(player).toHaveAttribute("data-playback-transport", "hls");
  const video = player.locator("video");
  await expectVideoAdvancing(video);
  await selectAndSee(video, "120p (H.264, video1)", "red");
  await selectAndSee(video, "120p (H.264, video2)", "blue");
  await player.hover();
  await player.locator('button[title="Settings"]').click();
  await page.getByRole("menuitemradio", { name: "Auto", exact: true }).click();
  await page.keyboard.press("Escape");
  await expectVideoAdvancing(video);
  await player.hover();
  await player.locator('button[title="Settings"]').click();
  await expect(
    page.getByRole("menuitemradio", { name: "Auto", exact: true }),
  ).toHaveAttribute("aria-checked", "true");
  await page.keyboard.press("Escape");
  expect(pageErrors).toEqual([]);
});

test("MoQ plays real video and audio, pauses, and releases resources on transport switch", async ({
  page,
  request,
}, testInfo) => {
  const pageErrors: string[] = [];
  page.on("pageerror", (error) => pageErrors.push(error.message));
  const network = await page.context().newCDPSession(page);
  await network.send("Network.enable");
  const established = new Set<string>();
  const closed = new Set<string>();
  network.on("Network.webTransportConnectionEstablished", ({ transportId }) =>
    established.add(transportId),
  );
  network.on("Network.webTransportClosed", ({ transportId }) =>
    closed.add(transportId),
  );
  const discovery = new URL(
    "/xrpc/place.stream.playback.getPlaybackServer",
    process.env.SERVER_URL,
  );
  discovery.searchParams.set("stream", process.env.ACCOUNT_DID!);
  await expect
    .poll(async () => {
      const response = await request.get(discovery.href);
      return response.ok() ? (await response.json()).moq?.name : undefined;
    })
    .toBe(`playback/${process.env.ACCOUNT_DID}`);

  await page.addInitScript(observeAudioOutput);
  await page.addInitScript(() => {
    window.moqVideoDecoders = [];
    const pressure = (window.moqVideoPressure = {
      longTasks: [] as { at: number; duration: number }[],
      taskLag: [] as { at: number; lag: number }[],
    });
    if (PerformanceObserver.supportedEntryTypes.includes("longtask")) {
      new PerformanceObserver((list) =>
        pressure.longTasks.push(
          ...list.getEntries().map((entry) => ({
            at: performance.timeOrigin + entry.startTime,
            duration: entry.duration,
          })),
        ),
      ).observe({ type: "longtask" });
    }
    let lastTask = performance.now();
    setInterval(() => {
      const at = performance.now();
      pressure.taskLag.push({
        at: performance.timeOrigin + at,
        lag: at - lastTask - 250,
      });
      lastTask = at;
    }, 250);
    const NativeDecoder = window.VideoDecoder;
    window.VideoDecoder = new Proxy(NativeDecoder, {
      construct(target, args, newTarget) {
        const options = args[0] as VideoDecoderInit;
        let record: NonNullable<Window["moqVideoDecoders"]>[number];
        const decoder = Reflect.construct(
          target,
          [
            {
              ...options,
              output(frame: VideoFrame) {
                record.outputs++;
                return options.output(frame);
              },
              error(error: DOMException) {
                record.errors.push(String(error));
                return options.error(error);
              },
            },
          ],
          newTarget,
        ) as VideoDecoder;
        record = { decoder, maxPending: 0, queues: [], outputs: 0, errors: [] };
        window.moqVideoDecoders!.push(record);
        const decode = decoder.decode;
        decoder.decode = function (chunk) {
          const result = Reflect.apply(decode, this, [chunk]);
          record.maxPending = Math.max(record.maxPending, this.decodeQueueSize);
          record.queues.push({
            at: performance.timeOrigin + performance.now(),
            pending: this.decodeQueueSize,
          });
          return result;
        };
        return decoder;
      },
    });
  });

  const player = await openLivePlayer(page);
  await expect(player).toHaveAttribute("data-playback-transport", "moq");
  const canvas = player.locator("canvas");
  await expect(canvas).toHaveCSS("opacity", "1");
  expect(established.size).toBeGreaterThan(0);
  const frame = () =>
    canvas.evaluate((element: HTMLCanvasElement) => element.toDataURL());
  const firstFrame = await frame();
  const advancingSince = Date.now();
  await expect.poll(frame).not.toBe(firstFrame);
  const frameInterval = Date.now() - advancingSince;

  await player.hover();
  await player.locator('button[title="Unmute"]').click();
  await expectAudioPlaying(page, testInfo);

  // The harness publisher keeps running while only the receiver's thread stalls.
  const audioDuringStall = await page.evaluate(async () => {
    const audible = () =>
      window.moqAudioAnalysers!.some((analyser) => {
        const samples = new Float32Array(analyser.fftSize);
        analyser.getFloatTimeDomainData(samples);
        return (
          analyser.context.state === "running" &&
          samples.some((sample) => Math.abs(sample) > 0.0001)
        );
      });
    const deadline = performance.now() + 2000;
    let buffer: NonNullable<Window["moqAudioBuffers"]>[number] | undefined;
    let headroomMs: number | undefined;
    do {
      buffer = window.moqAudioBuffers!.find(
        (output) =>
          output.context.state === "running" &&
          !output.stalled &&
          output.end !== undefined &&
          output.positions.length > 0,
      );
      headroomMs = buffer
        ? (buffer.end! - buffer.positions.at(-1)!.timestamp) / 1000
        : undefined;
      if (headroomMs !== undefined && headroomMs <= 150) break;
      await new Promise((resolve) => setTimeout(resolve, 5));
    } while (performance.now() < deadline);
    if (!buffer) throw new Error("No active native MoQ audio buffer");
    const positionCount = buffer.positions.length;
    const before = audible();
    const start = performance.now();
    while (performance.now() - start < 200) {
      // Native audio keeps rendering through actual main-thread pressure.
    }
    const after = audible();
    const blockedMs = performance.now() - start;
    // Deliver the worklet's native cursor reports queued during the stall.
    await new Promise((resolve) => setTimeout(resolve, 50));
    return {
      before,
      after,
      blockedMs,
      startedAt: performance.timeOrigin + start,
      headroomMs,
      positions: buffer.positions.slice(positionCount),
    };
  });
  await testInfo.attach("native-audio-main-thread-stall", {
    body: JSON.stringify(audioDuringStall),
    contentType: "application/json",
  });
  expect(audioDuringStall.before).toBe(true);
  expect(audioDuringStall.after).toBe(true);
  expect(audioDuringStall.positions.length).toBeGreaterThan(5);
  expect(
    audioDuringStall.positions.every(
      (position, index, positions) =>
        !position.stalled &&
        (index === 0 || position.timestamp > positions[index - 1].timestamp),
    ),
    "the native audio cursor advances throughout receiver pressure",
  ).toBe(true);
  const decoded = await page.evaluate(() =>
    window.moqVideoDecoders!.map(({ maxPending, queues, outputs, errors }) => ({
      maxPending,
      queues,
      outputs,
      errors,
    })),
  );
  await testInfo.attach("native-video-decode-pressure", {
    body: JSON.stringify({
      decoded,
      pressure: await page.evaluate(() => window.moqVideoPressure),
      controlledPressureStartedAt: audioDuringStall.startedAt,
      controlledPressureMs: audioDuringStall.blockedMs,
    }),
    contentType: "application/json",
  });
  expect(decoded.some(({ outputs }) => outputs > 0)).toBe(true);
  expect(decoded.every(({ errors }) => errors.length === 0)).toBe(true);
  // Keep coded work in the bounded media consumer until the codec can accept it.
  expect(
    Math.max(...decoded.map(({ maxPending }) => maxPending)),
  ).toBeLessThanOrEqual(1);

  await player.hover();
  await player.locator('button[title="Pause"]').click();
  await expect(player.locator('button[title="Play"]')).toBeVisible();
  let pausedFrame = await frame();
  let stableSamples = 0;
  await expect
    .poll(async () => {
      const current = await frame();
      const unchanged = current === pausedFrame;
      pausedFrame = current;
      stableSamples = unchanged ? stableSamples + 1 : 0;
      return stableSamples;
    })
    .toBeGreaterThanOrEqual(2);
  // Hold longer than the observed frame interval so ignored pauses fail.
  await page.waitForTimeout(Math.max(250, frameInterval * 2));
  expect(await frame()).toBe(pausedFrame);
  await player.locator('button[title="Play"]').click();
  await expect.poll(frame).not.toBe(pausedFrame);

  await player.hover();
  await player.locator('button[title="Settings"]').click();
  await page
    .getByRole("menuitemradio", { name: "Standard", exact: true })
    .click();
  await page.keyboard.press("Escape");
  await expect(player).toHaveAttribute("data-playback-transport", "hls");
  await expect
    .poll(() =>
      page.evaluate(() =>
        window.moqAudioAnalysers!.every(
          (analyser) => analyser.context.state === "closed",
        ),
      ),
    )
    .toBe(true);
  await expect
    .poll(() => [...established].every((id) => closed.has(id)))
    .toBe(true);
  await expect
    .poll(() =>
      page.evaluate(() =>
        window.moqVideoDecoders!.every(
          ({ decoder }) => decoder.state === "closed",
        ),
      ),
    )
    .toBe(true);
  const video = player.locator("video");
  await expectVideoAdvancing(video);
  expect(
    await video.evaluate((element: HTMLVideoElement) => element.muted),
  ).toBe(false);

  // Returning with audio already enabled exercises the catalog/buffer startup gate.
  await player.hover();
  await player.locator('button[title="Settings"]').click();
  await page
    .getByRole("menuitemradio", { name: "Low Latency", exact: true })
    .click();
  await page.keyboard.press("Escape");
  await expect(player).toHaveAttribute("data-playback-transport", "moq");
  await expect(canvas).toHaveCSS("opacity", "1");
  await expect(player.locator('button[title="Mute"]')).toBeVisible();
  await expectAudioPlaying(page, testInfo);
  await player.hover();
  await player.locator('button[title="Settings"]').click();
  await page
    .getByRole("menuitemcheckbox", { name: "Stats for Nerds", exact: true })
    .click();
  await page.keyboard.press("Escape");
  const stats = page.getByRole("dialog", {
    name: "Stats for Nerds",
    exact: true,
  });
  await expect(stats).toBeVisible();
  await expect(stats.locator("h3")).toHaveCSS("font-size", "12px");
  await expect(stats.locator("[data-stat] dd").first()).toHaveCSS(
    "font-size",
    "12px",
  );
  await page.screenshot({ path: testInfo.outputPath("stats-summary.png") });
  await expect(page.locator('[data-slot="sheet-overlay"]')).toHaveCount(0);
  const bounds = (await stats.boundingBox())!;
  const videoBounds = (await player.boundingBox())!;
  expect(bounds.width).toBeLessThanOrEqual(videoBounds.width / 2);
  expect(bounds.width * bounds.height).toBeLessThan(
    videoBounds.width * videoBounds.height * 0.3,
  );
  expect(
    await stats.evaluate(
      (element) => getComputedStyle(element).backgroundColor,
    ),
  ).toMatch(/0\.75|75%/);
  await expect(stats).toHaveCSS("backdrop-filter", "none");
  expect(
    (await stats
      .getByRole("button", { name: "Close", exact: true })
      .boundingBox())!.height,
  ).toBeGreaterThanOrEqual(
    (await page.evaluate(() => matchMedia("(pointer: fine)").matches))
      ? 24
      : 44,
  );
  for (const key of [
    "liveEdgeDelay",
    "playbackDelayMs",
    "publicationBatchMs",
  ]) {
    await expect(stats.locator(`[data-stat="${key}"] dd`)).toContainText(
      /-?\d+\.\d+ (ms|s)/,
    );
  }
  await expect
    .poll(() => stats.locator(".recharts-line-curve").count())
    .toBeGreaterThan(0);
  await expect(stats.locator(".recharts-surface")).toHaveCount(2);
  await page.screenshot({
    path: testInfo.outputPath("stats-diagnostics-moq.png"),
  });
  // The exposed video remains interactive while diagnostics are visible.
  await player.click({
    position: { x: videoBounds.width - 24, y: videoBounds.height / 2 },
  });
  await expect(player.locator('button[title="Play"]')).toBeVisible();
  await expect(stats).toBeVisible();
  await player.locator('button[title="Play"]').click();
  for (const key of ["roundTripTimeMs", "avSyncMs"]) {
    await expect(stats.locator(`[data-stat="${key}"] dd`)).toContainText(
      /-?\d+\.\d+ (ms|s)/,
    );
  }
  const avOffset = stats.locator('[data-stat="avSyncMs"] dd');
  await expect(avOffset).toHaveText(/^-?\d+\.\d ms$/);
  await expect
    .poll(async () => Math.abs(Number.parseFloat(await avOffset.innerText())))
    .toBeLessThan(1000);
  await expect(stats.locator('[data-stat="processingDelayMs"]')).toHaveCount(0);
  await expect(stats.locator("details")).toHaveCount(0);
  await expect(stats).toContainText("Not measured");
  await expect(stats).toContainText("MoQ");
  await expect(stats.getByText("Skipped", { exact: true })).toHaveCount(0);
  await expect(stats.getByText("FPS", { exact: true })).toHaveCount(0);
  await stats.getByRole("button", { name: "Close", exact: true }).focus();
  await page.keyboard.press("Escape");
  await expect(stats).not.toBeVisible();
  async function showStats() {
    await player.hover();
    await player.locator('button[title="Settings"]').click();
    await page
      .getByRole("menuitemcheckbox", { name: "Stats for Nerds", exact: true })
      .click();
    await page.keyboard.press("Escape");
    await expect(stats).toBeVisible();
  }
  await player.hover();
  await player.locator('button[title="Fullscreen"]').click();
  await expect
    .poll(() => page.evaluate(() => document.fullscreenElement !== null))
    .toBe(true);
  await showStats();
  expect(
    await stats.evaluate((element) =>
      document.fullscreenElement?.contains(element),
    ),
  ).toBe(true);
  const chartFrame = await frame();
  await expect.poll(frame).not.toBe(chartFrame);
  await expectAudioPlaying(page, testInfo);
  await stats.getByRole("button", { name: "Close", exact: true }).click();
  await expect(stats).not.toBeVisible();
  await player.hover();
  await player.locator('button[title="Exit fullscreen"]').click();
  await expect
    .poll(() => page.evaluate(() => document.fullscreenElement))
    .toBeNull();
  // A transport change must not graph the previous transport's readings.
  await player.hover();
  await player.locator('button[title="Settings"]').click();
  await page
    .getByRole("menuitemradio", { name: "Standard", exact: true })
    .click();
  await page.keyboard.press("Escape");
  await expect(player).toHaveAttribute("data-playback-transport", "hls");
  await expectVideoAdvancing(player.locator("video"));
  await showStats();
  await expect(stats.locator('[data-stat="publicationBatchMs"]')).toHaveCount(
    0,
  );
  await expect
    .poll(() => stats.locator(".recharts-line-curve").count())
    .toBeGreaterThan(0);
  await page.setViewportSize({ width: 390, height: 844 });
  const panelBounds = (await stats.boundingBox())!;
  const mobilePlayer = (await player.boundingBox())!;
  expect(panelBounds.width).toBeLessThanOrEqual(mobilePlayer.width / 2);
  expect(panelBounds.width * panelBounds.height).toBeLessThan(
    mobilePlayer.width * mobilePlayer.height * 0.5,
  );
  expect(
    await stats.evaluate(
      (element) => element.scrollWidth <= element.clientWidth,
    ),
  ).toBe(true);
  await page.screenshot({
    path: testInfo.outputPath("stats-diagnostics-mobile.png"),
  });
  // Inline players can be much shorter than the fullscreen embed.
  await player.evaluate((element) => {
    element.style.height = "220px";
  });
  const shortPlayer = (await player.boundingBox())!;
  const shortStats = (await stats.boundingBox())!;
  expect(shortStats.width * shortStats.height).toBeLessThan(
    (shortPlayer.width * shortPlayer.height) / 2,
  );
  expect(shortStats.y + shortStats.height).toBeLessThanOrEqual(
    shortPlayer.y + shortPlayer.height,
  );
  await page.screenshot({
    path: testInfo.outputPath("stats-diagnostics-inline.png"),
  });
  await stats.locator("[data-stats-scroll]").evaluate((element) => {
    element.scrollTop = element.scrollHeight;
  });
  await expect(
    stats.getByRole("button", { name: "Close", exact: true }),
  ).toBeVisible();
  await stats.getByRole("button", { name: "Close", exact: true }).click();
  await expect(stats).not.toBeVisible();
  expect(pageErrors).toEqual([]);
});

test("MoQ connection loss falls back to a live transport that decodes video", async ({
  page,
}) => {
  await page.addInitScript(() => {
    const NativeWebTransport = window.WebTransport;
    const transports: WebTransport[] = [];
    let connectionLost = false;
    window.WebTransport = new Proxy(NativeWebTransport, {
      construct(target, args, newTarget) {
        const transport = Reflect.construct(
          target,
          args,
          newTarget,
        ) as WebTransport;
        transports.push(transport);
        // Keep real sessions unavailable across the client's reconnect attempts.
        if (connectionLost) {
          void transport.ready.then(
            () => transport.close(),
            () => {},
          );
        }
        return transport;
      },
    });
    window.loseMoqConnection = async () => {
      if (transports.length === 0)
        throw new Error("No native MoQ transport was created");
      connectionLost = true;
      await Promise.all(
        transports.map((transport) => {
          transport.close();
          return transport.closed;
        }),
      );
    };
  });
  const player = await openLivePlayer(page);
  await expect(player).toHaveAttribute("data-playback-transport", "moq");
  await expect(player.locator("canvas")).toHaveCSS("opacity", "1");
  await page.evaluate(() => window.loseMoqConnection!());
  await expect(player).toHaveAttribute(
    "data-playback-transport",
    /^(webrtc|hls)$/,
  );
  await expectVideoAdvancing(player.locator("video"));
});

test("WebRTC exposes receiver processing, decode and jitter buffer timing", async ({
  page,
}, testInfo) => {
  await page.addInitScript(() => {
    Object.defineProperty(window, "WebTransport", { value: undefined });
    const state = {
      paused: false,
      peers: new Map<RTCPeerConnection, number>(),
    };
    window.webrtcStatsTest = state;
    const getStats = RTCPeerConnection.prototype.getStats;
    RTCPeerConnection.prototype.getStats = function (...args) {
      state.peers.set(this, (state.peers.get(this) ?? 0) + 1);
      if (state.paused)
        return Promise.reject(new DOMException("Stats reporting suspended"));
      return getStats.apply(this, args);
    };
  });
  const player = await openLivePlayer(page);
  await expect(player).toHaveAttribute("data-playback-transport", "webrtc");
  await expectVideoAdvancing(player.locator("video"));
  await player.hover();
  await player.locator('button[title="Settings"]').click();
  await page
    .getByRole("menuitemcheckbox", { name: "Stats for Nerds", exact: true })
    .click();
  await page.keyboard.press("Escape");
  const stats = page.getByRole("dialog", {
    name: "Stats for Nerds",
    exact: true,
  });
  for (const key of ["processingDelayMs", "decodeTimeMs", "jitterBufferMs"]) {
    const row = stats.locator(`[data-stat="${key}"]`);
    await expect(row).toContainText(/\d+\.\d ms/);
    await expect
      .poll(() => stats.locator(".recharts-line-curve").count())
      .toBeGreaterThan(0);
  }
  await expect(stats.locator('[data-stat="publicationBatchMs"]')).toHaveCount(
    0,
  );
  await page.screenshot({
    path: testInfo.outputPath("webrtc-timing-stats.png"),
  });
  await page.evaluate(() => {
    window.webrtcStatsTest!.paused = true;
  });
  await expect(
    stats.getByText("Readings are stale. Waiting for a fresh sample.").first(),
  ).toBeVisible();
  await expect(stats.locator(".recharts-surface")).toHaveCount(0);
  await expectVideoAdvancing(player.locator("video"));
  await page.evaluate(() => {
    window.webrtcStatsTest!.paused = false;
  });
  await expect(stats.locator("[data-stat=processingDelayMs]")).toContainText(
    /\d+\.\d ms/,
  );
  await expect
    .poll(() => stats.locator(".recharts-line-curve").count())
    .toBeGreaterThan(0);

  await page.evaluate(() => {
    const peers = window.webrtcStatsTest!.peers;
    const peer = [...peers.keys()].find(
      (pc) => pc.connectionState === "connected",
    )!;
    peer.close();
    // A local close does not emit the state event that network failure emits.
    peer.dispatchEvent(new Event("connectionstatechange"));
  });
  // Embedded viewers fall back to HLS; the backend reconnect path is used
  // by streamer monitors and is covered by the polling lifecycle test.
  await expect(player).toHaveAttribute("data-playback-transport", "hls");
  await expect
    .poll(() =>
      player
        .locator("video")
        .evaluate((video: HTMLVideoElement) => video.srcObject === null),
    )
    .toBe(true);
  const oldPolls = await page.evaluate(
    () => [...window.webrtcStatsTest!.peers.values()][0],
  );
  await expectVideoAdvancing(player.locator("video"));
  expect(
    await page.evaluate(() => [...window.webrtcStatsTest!.peers.values()][0]),
  ).toBe(oldPolls);
});

test("a stats download failure leaves live playback running", async ({
  page,
}) => {
  let blocked = 0;
  await page.route("**/stats-overlay-*.js", async (route) => {
    blocked++;
    await route.abort("failed");
  });
  const player = await openLivePlayer(page);
  const canvas = player.locator("canvas");
  await expect(canvas).toHaveCSS("opacity", "1");
  const frame = () =>
    canvas.evaluate((element: HTMLCanvasElement) => element.toDataURL());
  const initial = await frame();
  await expect.poll(frame).not.toBe(initial);
  expect(blocked).toBe(0);
  await player.hover();
  await player.locator('button[title="Settings"]').click();
  await page
    .getByRole("menuitemcheckbox", { name: "Stats for Nerds", exact: true })
    .click();
  await page.keyboard.press("Escape");
  const stats = page.getByRole("dialog", {
    name: "Stats for Nerds",
    exact: true,
  });
  await expect(stats.getByRole("alert")).toBeVisible();
  expect(blocked).toBeGreaterThan(0);
  const failed = await frame();
  await expect.poll(frame).not.toBe(failed);
  await stats.getByRole("button", { name: "Close", exact: true }).click();
  await expect(stats).not.toBeVisible();
  const closed = await frame();
  await expect.poll(frame).not.toBe(closed);
});
