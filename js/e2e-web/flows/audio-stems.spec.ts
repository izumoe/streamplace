import { expect, test } from "@playwright/test";
import { readFileSync, writeFileSync } from "node:fs";

for (const transport of ["webrtc", "moq"] as const) {
  test(`received audio stems: ${transport} full mix and solo controls`, async ({
    page,
  }, testInfo) => {
    test.skip(
      process.env.E2E_AUDIO_STEMS !== "1",
      "requires the signed grouped-audio fixture publisher",
    );
    test.setTimeout(120_000);
    const server = process.env.SERVER2_URL!;
    const did = process.env.ACCOUNT_DID!;
    await expect
      .poll(
        async () => {
          const response = await fetch(
            `${server}/xrpc/place.stream.live.getLiveUsers?limit=50`,
          );
          return response.ok
            ? (((await response.json()) as { streams?: unknown[] }).streams
                ?.length ?? 0)
            : 0;
        },
        { timeout: 90_000, intervals: [1000] },
      )
      .toBeGreaterThan(0);

    await page.addInitScript((selected) => {
      localStorage.setItem("player-transport", selected);
      if (selected === "webrtc")
        Object.defineProperty(window, "WebTransport", { value: undefined });
      type Meter = {
        context: AudioContext;
        analyser?: AnalyserNode;
        gains: GainNode[];
        sources: MediaStreamAudioSourceNode[];
      };
      const meters: Meter[] = [];
      const playouts: HTMLAudioElement[] = [];
      Object.assign(window, { __stemMeters: meters, __stemPlayouts: playouts });
      const createElement = document.createElement.bind(document);
      document.createElement = ((
        tag: string,
        options?: ElementCreationOptions,
      ) => {
        const element = createElement(tag, options);
        if (tag === "audio") playouts.push(element as HTMLAudioElement);
        return element;
      }) as typeof document.createElement;
      const peers: RTCPeerConnection[] = [];
      Object.assign(window, { __stemPeers: peers });
      const NativePeer = RTCPeerConnection;
      window.RTCPeerConnection = new Proxy(NativePeer, {
        construct(target, args) {
          const peer = new target(...args);
          peers.push(peer);
          return peer;
        },
      });
      const connect = AudioNode.prototype.connect;
      AudioNode.prototype.connect = function (
        this: AudioNode,
        ...args: [AudioNode | AudioParam, number?, number?]
      ) {
        const meter = meters.find((item) => item.context === this.context);
        if (meter && args[0] === this.context.destination) {
          const analyser = (meter.analyser ??= this.context.createAnalyser());
          analyser.fftSize = 8192;
          analyser.smoothingTimeConstant = 0;
          Reflect.apply(connect, this, [analyser]);
        }
        return Reflect.apply(connect, this, args);
      } as typeof AudioNode.prototype.connect;
      const Native = AudioContext;
      window.AudioContext = new Proxy(Native, {
        construct(target, args) {
          const context = new target(...args);
          const meter: Meter = { context, gains: [], sources: [] };
          meters.push(meter);
          const createGain = context.createGain.bind(context);
          context.createGain = () => {
            const gain = createGain();
            meter.gains.push(gain);
            return gain;
          };
          const createSource = context.createMediaStreamSource.bind(context);
          context.createMediaStreamSource = (stream) => {
            const source = createSource(stream);
            meter.sources.push(source);
            return source;
          };
          return context;
        },
      });
    }, transport);
    const peerMoq = process.env.E2E_PEER_MOQ_URL;
    if (transport === "moq") {
      if (!peerMoq) throw new Error("E2E_PEER_MOQ_URL is missing");
      const discovery = new URL(
        "/xrpc/place.stream.playback.getPlaybackServer",
        server,
      );
      discovery.searchParams.set("stream", did);
      await expect
        .poll(async () => {
          const response = await fetch(discovery);
          if (!response.ok) return null;
          const body = await response.json();
          return body.moq && { url: body.moq.url, name: body.moq.name };
        })
        .toEqual({ url: peerMoq, name: `playback/${did}` });
    }
    await page.goto(`${server}/${did}`);
    const player = page.locator("[data-playback-transport]").first();
    await expect(player).toHaveAttribute("data-playback-transport", transport);
    await player.hover();
    const unmute = page
      .getByRole("button", { name: "Unmute", exact: true })
      .first();
    if (await unmute.isVisible()) await unmute.click();
    const readAudio = () =>
      page.evaluate(() => {
        const meters = (
          window as unknown as {
            __stemMeters: { context: AudioContext; analyser?: AnalyserNode }[];
          }
        ).__stemMeters;
        const active = meters.filter(
          (meter) => meter.context.state === "running",
        );
        const meter = active.find((item) => item.analyser);
        if (!meter?.analyser)
          return { contexts: active.length, voice: -200, music: -200 };
        const bins = new Float32Array(meter.analyser.frequencyBinCount);
        meter.analyser.getFloatFrequencyData(bins);
        const at = (hz: number) => {
          const index = Math.round(
            (hz / meter.context.sampleRate) * meter.analyser!.fftSize,
          );
          return Math.max(...bins.slice(index - 1, index + 2));
        };
        return { contexts: active.length, voice: at(440), music: at(880) };
      });
    try {
      await expect
        .poll(async () =>
          Math.min((await readAudio()).voice, (await readAudio()).music),
        )
        .toBeGreaterThan(-55);
    } catch (error) {
      const diagnostic = await page.evaluate(async () => {
        const state = window as unknown as {
          __stemMeters: {
            context: AudioContext;
            gains: GainNode[];
            sources: MediaStreamAudioSourceNode[];
          }[];
          __stemPeers: RTCPeerConnection[];
        };
        const video = document.querySelector("video");
        return {
          video: video && {
            paused: video.paused,
            muted: video.muted,
            volume: video.volume,
            time: video.currentTime,
            tracks:
              video.srcObject instanceof MediaStream
                ? video.srcObject.getTracks().map((t) => ({
                    id: t.id,
                    kind: t.kind,
                    enabled: t.enabled,
                    muted: t.muted,
                    state: t.readyState,
                  }))
                : [],
          },
          meters: state.__stemMeters.map((m) => ({
            state: m.context.state,
            time: m.context.currentTime,
            gains: m.gains.map((g) => g.gain.value),
            sources: m.sources.map((s) =>
              s.mediaStream.getAudioTracks().map((t) => ({
                id: t.id,
                enabled: t.enabled,
                muted: t.muted,
                state: t.readyState,
              })),
            ),
          })),
          peers: await Promise.all(
            state.__stemPeers.map(async (peer) => {
              const report = await peer.getStats();
              const inbound: unknown[] = [];
              report.forEach((row) => {
                if (row.type === "inbound-rtp")
                  inbound.push({
                    kind: row.kind,
                    mid: row.mid,
                    packets: row.packetsReceived,
                    totalAudioEnergy: row.totalAudioEnergy,
                    totalSamplesReceived: row.totalSamplesReceived,
                    frames: row.framesDecoded,
                  });
              });
              return { state: peer.connectionState, inbound };
            }),
          ),
        };
      });
      await testInfo.attach("audio-failure-diagnostic", {
        body: JSON.stringify(diagnostic, null, 2),
        contentType: "application/json",
      });
      throw error;
    }
    const full = await readAudio();
    const picture = player
      .locator(transport === "moq" ? "canvas" : "video")
      .first();
    await expect(picture).toBeVisible();
    const readPicture = () =>
      picture.evaluate((element) => {
        if (element instanceof HTMLCanvasElement) return element.toDataURL();
        return String(
          (element as HTMLVideoElement).getVideoPlaybackQuality()
            .totalVideoFrames,
        );
      });
    const beforePicture = await readPicture();

    expect(full.contexts).toBe(1);
    await player.hover();
    await player.getByRole("button", { name: "Settings", exact: true }).click();
    const music = page.getByRole("slider", { name: "Music", exact: true });
    await expect(music).toBeVisible();
    await expect(
      page.getByRole("slider", { name: "Voice", exact: true }),
    ).toBeVisible();
    for (
      let step = 0;
      step < 8 &&
      !(await music.evaluate((element) => element === document.activeElement));
      step++
    ) {
      await page.keyboard.press("Tab");
    }
    await expect(music).toBeFocused();
    await music.press("Home");
    await expect
      .poll(async () => (await readAudio()).music)
      .toBeLessThan(full.music - 20);
    const solo = await readAudio();
    expect(solo.voice).toBeGreaterThan(full.voice - 3);
    expect(solo.contexts).toBe(1);
    await page
      .getByRole("button", { name: "Use full mix", exact: true })
      .click();
    await expect
      .poll(async () => (await readAudio()).music)
      .toBeGreaterThan(full.music - 3);
    await expect(player).toHaveAttribute("data-playback-transport", transport);
    await expect.poll(readPicture).not.toBe(beforePicture);
    const audioProof = testInfo.outputPath("audio-mix-frequency-levels.json");
    writeFileSync(
      audioProof,
      JSON.stringify({ full, solo, restored: await readAudio() }, null, 2),
    );
    await testInfo.attach("audio-mix-frequency-levels", {
      path: audioProof,
      contentType: "application/json",
    });
    const recordsURL = new URL(
      "/xrpc/com.atproto.repo.listRecords",
      process.env.PDS_HTTP_URL!,
    );
    recordsURL.search = new URLSearchParams({
      repo: did,
      collection: "place.stream.broadcast.origin",
    }).toString();
    const originsResponse = await fetch(recordsURL);
    expect(originsResponse.ok).toBe(true);
    const origins = (await originsResponse.json()) as {
      records: { value: { streamer: string; moqURL?: string } }[];
    };
    expect(
      origins.records.some(
        ({ value }) => value.streamer === did && !!value.moqURL,
      ),
    ).toBe(true);
    expect(origins.records.some(({ value }) => value.moqURL === peerMoq)).toBe(
      false,
    );
    expect(readFileSync(process.env.E2E_LOGFILE!, "utf8")).toContain(
      "syndication: connected to origin (moq)",
    );
    await page.keyboard.press("Escape");
    await page.getByRole("button", { name: "Home", exact: true }).click();
    await expect
      .poll(() =>
        page.evaluate(() => {
          const state = window as unknown as {
            __stemMeters: { context: AudioContext }[];
            __stemPlayouts: HTMLAudioElement[];
          };
          return {
            contexts: state.__stemMeters.filter(
              (meter) => meter.context.state !== "closed",
            ).length,
            playouts: state.__stemPlayouts.filter(
              (element) => !element.paused || element.srcObject !== null,
            ).length,
          };
        }),
      )
      .toEqual({ contexts: 0, playouts: 0 });
  });
}

test("received audio stems: all AVC WebRTC rungs keep progressing on the peer", async ({
  page,
}, testInfo) => {
  test.skip(
    process.env.E2E_AUDIO_STEMS !== "1",
    "requires the signed grouped-audio fixture publisher",
  );
  test.setTimeout(100_000);
  const server = process.env.SERVER2_URL!,
    did = process.env.ACCOUNT_DID!;
  await page.goto(`${server}/api/healthz`);
  const progress = await page.evaluate(
    async ({ server, did }) => {
      return Promise.all(
        [
          { name: "source", width: 1920, height: 1080 },
          { name: "720p", width: 1280, height: 720 },
          { name: "360p", width: 640, height: 360 },
        ].map(async (rung) => {
          const pc = new RTCPeerConnection({ bundlePolicy: "max-bundle" });
          const video = document.createElement("video");
          video.muted = true;
          document.body.append(video);
          const stream = new MediaStream();
          pc.ontrack = ({ track }) => {
            if (track.kind === "video" || track.id === "audio")
              stream.addTrack(track);
            video.srcObject = stream;
            void video.play().catch(() => {});
          };
          try {
            pc.addTransceiver("video", { direction: "recvonly" });
            for (let i = 0; i < 3; i++)
              pc.addTransceiver("audio", { direction: "recvonly" });
            await pc.setLocalDescription(await pc.createOffer());
            await new Promise<void>((resolve) => {
              const done = () => {
                clearTimeout(timer);
                pc.removeEventListener("icegatheringstatechange", change);
                resolve();
              };
              const change = () => {
                if (pc.iceGatheringState === "complete") done();
              };
              const timer = setTimeout(done, 1500);
              pc.addEventListener("icegatheringstatechange", change);
              change();
            });
            const params = new URLSearchParams({
              streamer: did,
              rendition: rung.name,
            });
            const response = await fetch(
              `${server}/xrpc/place.stream.playback.whep?${params}`,
              {
                method: "POST",
                headers: { "Content-Type": "application/sdp" },
                body: pc.localDescription!.sdp,
                signal: AbortSignal.timeout(15_000),
              },
            );
            if (!response.ok) throw new Error(`WHEP ${response.status}`);
            await pc.setRemoteDescription({
              type: "answer",
              sdp: await response.text(),
            });
            const samples = [];
            for (let second = 0; second < 26; second++) {
              await new Promise((resolve) => setTimeout(resolve, 1000));
              const audio: Record<string, number> = {};
              const report = await pc.getStats();
              let frames = 0,
                width = 0,
                height = 0;
              report.forEach((row) => {
                if (row.type !== "inbound-rtp") return;
                if (row.kind === "video") {
                  frames = row.framesDecoded ?? 0;
                  width = row.frameWidth ?? 0;
                  height = row.frameHeight ?? 0;
                } else if (row.kind === "audio")
                  audio[row.mid ?? row.id] = row.packetsReceived ?? 0;
              });
              samples.push({
                at: performance.now(),
                frames,
                width,
                height,
                audio,
              });
            }
            return { rung, samples };
          } finally {
            pc.close();
            video.remove();
          }
        }),
      );
    },
    { server, did },
  );
  for (const { rung, samples } of progress) {
    const before = samples[10],
      after = samples.at(-1)!;
    expect([after.width, after.height], rung.name).toEqual([
      rung.width,
      rung.height,
    ]);
    expect(
      (after.frames - before.frames) / ((after.at - before.at) / 1000),
      rung.name,
    ).toBeGreaterThan(25);
    expect(Object.keys(after.audio), rung.name).toHaveLength(3);
    for (const [id, packets] of Object.entries(after.audio))
      expect(packets, `${rung.name} audio ${id}`).toBeGreaterThan(
        before.audio[id] ?? 0,
      );
  }
  await testInfo.attach("grouped-rendition-progress", {
    body: JSON.stringify(progress),
    contentType: "application/json",
  });
  expect(readFileSync(process.env.E2E_LOGFILE!, "utf8")).toContain(
    "syndication: connected to origin (moq)",
  );
});
