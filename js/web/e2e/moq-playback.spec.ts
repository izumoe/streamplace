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
    loseMoqConnection?: () => Promise<void>;
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
  await expect(stats.getByText("MoQ", { exact: true })).toBeVisible();
  await expect(stats.getByText("Skipped", { exact: true })).toHaveCount(0);
  await expect(stats.getByText("FPS", { exact: true })).toHaveCount(0);
  await expect(stats.getByText("Bitrate", { exact: true })).toHaveCount(0);
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
