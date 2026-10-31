import type { makeLatencyMarker } from "./live-latency-marker";

// Injected into one page so publisher, iframe observations and WHEP callbacks
// share the publisher's monotonic clock. No application code is replaced.
export function createLatencyExperiment(
  marker: ReturnType<typeof makeLatencyMarker>,
  server: string,
  did: string,
  includeMoq = true,
) {
  const generated = new Map<number, number>();
  const source = document.createElement("canvas");
  source.width = 640;
  source.height = 360;
  source.style.cssText = "width:320px;height:180px;display:block";
  const context = source.getContext("2d")!;
  const stream = source.captureStream(0);
  const track = stream.getVideoTracks()[0] as CanvasCaptureMediaStreamTrack;
  const audio = new AudioContext({ sampleRate: 48000 });
  const tone = audio.createOscillator();
  const destination = audio.createMediaStreamDestination();
  tone.frequency.value = 440;
  tone.connect(destination);
  stream.addTrack(destination.stream.getAudioTracks()[0]);
  const publisher = new RTCPeerConnection({ bundlePolicy: "max-bundle" });
  const receiver = new RTCPeerConnection({ bundlePolicy: "max-bundle" });
  const video = document.createElement("video");
  video.width = 640;
  video.height = 360;
  video.autoplay = true;
  video.muted = true;
  video.playsInline = true;
  const iframe = document.createElement("iframe");
  iframe.width = "640";
  iframe.height = "360";
  iframe.allow = "autoplay; fullscreen";
  iframe.style.border = "0";
  const viewers = document.createElement("div");
  viewers.style.cssText = "display:flex;gap:16px";
  viewers.append(...(includeMoq ? [iframe, video] : [video]));
  const whepAnalyser = includeMoq ? undefined : audio.createAnalyser();
  if (whepAnalyser) {
    whepAnalyser.connect(audio.destination);
  }
  document.body.append(source, viewers);

  type Frame = {
    id: number | null;
    generatedAt: number | null;
    display: number;
    callbackNow: number;
    callbackGapMs: number | null;
    readbackMs: number;
    latencyMs: number | null;
    changed: boolean;
    metadata?: VideoFrameCallbackMetadata;
  };
  const moq: Frame[] = [],
    whep: Frame[] = [];
  const events: { name: string; at: number }[] = [];
  const negotiations: { name: string; at: number; status: number }[] = [];
  const rtc: Awaited<ReturnType<typeof stats>>[] = [];
  const times = {
    publishStarted: null as number | null,
    sourceReady: null as number | null,
    moqStarted: null as number | null,
    whepStarted: null as number | null,
    unmuted: null as number | null,
    steadyStart: null as number | null,
    steadyEnd: null as number | null,
  };
  let paintTimer: ReturnType<typeof setInterval> | undefined;
  let animation = 0,
    videoCallback = 0;
  let stopped = false;
  let id = 0;
  const delay = (ms: number) =>
    new Promise((resolve) => setTimeout(resolve, ms));
  async function until(
    check: () => boolean | Promise<boolean>,
    name: string,
    timeout = 20_000,
  ) {
    const deadline = performance.now() + timeout;
    while (!(await check())) {
      if (performance.now() >= deadline) throw new Error(`timed out: ${name}`);
      await delay(50);
    }
  }
  function paint() {
    context.fillStyle = "#204060";
    context.fillRect(0, 0, 640, 360);
    for (let square = 0; square < 12; square++) {
      context.fillStyle = `hsl(${square * 30},80%,55%)`;
      context.fillRect(
        (id * 5 + square * 53) % 640,
        170 + ((id + square * 19) % 140),
        44,
        44,
      );
    }
    marker.encode(id).forEach((bit, index) => {
      context.fillStyle = bit ? "white" : "black";
      context.fillRect(
        marker.x + (index % marker.columns) * marker.cellSize,
        marker.y + Math.floor(index / marker.columns) * marker.cellSize,
        marker.cellSize,
        marker.cellSize,
      );
    });
    generated.set(id++, performance.now());
    track.requestFrame();
  }
  function reader(frames: Frame[]) {
    const scratch = document.createElement("canvas");
    scratch.width = marker.columns;
    scratch.height = marker.bits / marker.columns;
    const ctx = scratch.getContext("2d", { willReadFrequently: true })!;
    ctx.imageSmoothingEnabled = false;
    let previousId: number | null = null,
      previousCallback: number | null = null;
    return (
      surface: HTMLCanvasElement | HTMLVideoElement,
      display: number,
      metadata?: VideoFrameCallbackMetadata,
    ) => {
      const callbackNow = performance.now();
      const width =
        surface instanceof HTMLVideoElement
          ? surface.videoWidth
          : surface.width;
      const height =
        surface instanceof HTMLVideoElement
          ? surface.videoHeight
          : surface.height;
      if (!width || !height) return;
      ctx.drawImage(
        surface,
        (marker.x * width) / 640,
        (marker.y * height) / 360,
        (marker.columns * marker.cellSize * width) / 640,
        (scratch.height * marker.cellSize * height) / 360,
        0,
        0,
        scratch.width,
        scratch.height,
      );
      const pixels = ctx.getImageData(0, 0, scratch.width, scratch.height).data;
      const brightness = [];
      for (let pixel = 0; pixel < pixels.length; pixel += 4) {
        brightness.push(
          (pixels[pixel] + pixels[pixel + 1] + pixels[pixel + 2]) / 3,
        );
      }
      const decoded = marker.decode(brightness);
      const generatedAt =
        decoded === null ? null : (generated.get(decoded) ?? null);
      const changed = decoded !== null && decoded !== previousId;
      if (decoded !== null) previousId = decoded;
      frames.push({
        id: decoded,
        generatedAt,
        display,
        callbackNow,
        callbackGapMs:
          previousCallback === null ? null : callbackNow - previousCallback,
        readbackMs: performance.now() - callbackNow,
        latencyMs: generatedAt === null ? null : display - generatedAt,
        changed,
        metadata,
      });
      previousCallback = callbackNow;
    };
  }
  function audioSample(analyser: AnalyserNode) {
    const samples = new Float32Array(analyser.fftSize);
    analyser.getFloatTimeDomainData(samples);
    const context = analyser.context as AudioContext;
    return {
      state: context.state,
      currentTime: context.currentTime,
      sampleRate: context.sampleRate,
      baseLatency: context.baseLatency,
      outputLatency: context.outputLatency,
      peak: samples.reduce(
        (peak, sample) => Math.max(peak, Math.abs(sample)),
        0,
      ),
    };
  }
  function audioOutput() {
    return iframe.contentWindow?.moqAudioAnalysers?.map(audioSample) ?? [];
  }
  function whepAudioOutput() {
    return whepAnalyser ? [audioSample(whepAnalyser)] : [];
  }
  async function stats() {
    async function reports(peer: RTCPeerConnection) {
      const results: Record<string, unknown>[] = [];
      (await peer.getStats()).forEach((report) => {
        if (
          ["inbound-rtp", "outbound-rtp", "media-source", "codec"].includes(
            report.type,
          )
        )
          results.push({ ...report });
      });
      return results;
    }
    const publisherReports = await reports(publisher);
    const receiverReports = await reports(receiver);
    return {
      at: performance.now(),
      publisher: publisherReports,
      whep: receiverReports,
      audio: audioOutput(),
      whepAudio: whepAudioOutput(),
      moqTransport: iframe.contentDocument
        ?.querySelector("[data-playback-transport]")
        ?.getAttribute("data-playback-transport"),
      crossOriginIsolated: iframe.contentWindow?.crossOriginIsolated,
    };
  }
  async function exchange(
    name: string,
    peer: RTCPeerConnection,
    endpoint: string,
    key?: string,
  ) {
    const offer = await peer.createOffer();
    const opus = offer.sdp!.match(/^a=rtpmap:(\d+) opus\//im)?.[1];
    if (!opus) throw new Error(`${name}: no Opus codec`);
    const fmtp = new RegExp(`^a=fmtp:${opus} ([^\\r\\n]*)`, "m");
    if (!fmtp.test(offer.sdp!)) throw new Error(`${name}: no Opus parameters`);
    offer.sdp = offer.sdp!.replace(
      fmtp,
      (_, parameters: string) =>
        `a=fmtp:${opus} ${parameters
          .split(";")
          .filter((parameter) => !parameter.startsWith("stereo="))
          .concat("stereo=1")
          .join(";")}`,
    );
    await peer.setLocalDescription(offer);
    await until(
      () => peer.iceGatheringState === "complete",
      `${name} ICE gathering`,
      5000,
    );
    const response = await fetch(endpoint, {
      method: "POST",
      headers: {
        "content-type": "application/sdp",
        ...(key ? { Authorization: `Bearer ${key}` } : {}),
      },
      body: peer.localDescription!.sdp,
      signal: AbortSignal.timeout(20_000),
    });
    negotiations.push({ name, at: performance.now(), status: response.status });
    if (!response.ok) throw new Error(`${name}: HTTP ${response.status}`);
    await peer.setRemoteDescription({
      type: "answer",
      sdp: await response.text(),
    });
    await until(
      () => peer.connectionState === "connected",
      `${name} connection`,
    );
  }
  for (const [peer, name] of [
    [publisher, "publisher"],
    [receiver, "whep"],
  ] as const) {
    peer.addEventListener("connectionstatechange", () =>
      events.push({
        name: `${name}:${peer.connectionState}`,
        at: performance.now(),
      }),
    );
  }
  return {
    async start(key: string) {
      times.publishStarted = performance.now();
      tone.start();
      await audio.resume();
      paint();
      paintTimer = setInterval(paint, 1000 / 30);
      for (const kind of ["audio", "video"] as const) {
        const sender = publisher.addTransceiver(
          stream.getTracks().find((track) => track.kind === kind)!,
          { direction: "sendrecv" },
        );
        const codecs = RTCRtpSender.getCapabilities(kind)!.codecs.filter(
          (codec) =>
            codec.mimeType.toLowerCase() ===
            (kind === "audio" ? "audio/opus" : "video/h264"),
        );
        if (!codecs.length) throw new Error(`publisher: no ${kind} codec`);
        sender.setCodecPreferences(codecs);
      }
      await exchange("WHIP", publisher, `${server}/api/ingest/webrtc`, key);
      if (includeMoq)
        await until(
          async () => {
            const response = await fetch(
              `${server}/xrpc/place.stream.playback.getPlaybackServer?stream=${encodeURIComponent(did)}`,
              { signal: AbortSignal.timeout(3000) },
            );
            if (!response.ok) return false;
            return (await response.json()).moq?.name === `playback/${did}`;
          },
          "published MoQ origin",
          30_000,
        );
      times.sourceReady = performance.now();
      rtc.push(await stats());
    },
    async openViewers() {
      const readMoq = reader(moq),
        readWhep = reader(whep);
      function observeMoq() {
        if (stopped) return;
        const surface = iframe.contentDocument?.querySelector("canvas");
        // The iframe clock can have a different origin. Observe on the source clock.
        if (surface) readMoq(surface, performance.now());
        animation = requestAnimationFrame(observeMoq);
      }
      function observeWhep(_now: number, metadata: VideoFrameCallbackMetadata) {
        if (stopped) return;
        readWhep(video, metadata.expectedDisplayTime, metadata);
        videoCallback = video.requestVideoFrameCallback(observeWhep);
      }
      if (includeMoq) {
        times.moqStarted = performance.now();
        iframe.src = `${server}/embed/${encodeURIComponent(did)}/`;
        animation = requestAnimationFrame(observeMoq);
      }
      times.whepStarted = performance.now();
      receiver.ontrack = (event) => {
        video.srcObject = event.streams[0];
        if (whepAnalyser && event.track.kind === "audio") {
          audio
            .createMediaStreamSource(new MediaStream([event.track]))
            .connect(whepAnalyser);
        }
      };
      receiver.addTransceiver("video", { direction: "recvonly" });
      receiver.addTransceiver("audio", { direction: "recvonly" });
      videoCallback = video.requestVideoFrameCallback(observeWhep);
      const endpoint = new URL("/xrpc/place.stream.playback.whep", server);
      endpoint.searchParams.set("streamer", did);
      endpoint.searchParams.set("rendition", "source");
      await exchange("WHEP", receiver, endpoint.href);
      await video.play();
      await until(
        () =>
          (includeMoq ? [moq, whep] : [whep]).every((frames) =>
            frames.some((frame) => frame.latencyMs !== null),
          ),
        "first real markers",
        30_000,
      );
      rtc.push(await stats());
    },
    async measure() {
      times.unmuted = performance.now();
      await until(
        () =>
          (includeMoq ? audioOutput() : whepAudioOutput()).some(
            (output) => output.state === "running" && output.peak > 0.0001,
          ),
        includeMoq ? "real MoQ speaker samples" : "real WHEP speaker samples",
      );
      await delay(10_000);
      times.steadyStart = performance.now();
      times.steadyEnd = times.steadyStart + 20_000;
      while (performance.now() < times.steadyEnd) {
        rtc.push(await stats());
        await delay(
          Math.min(1000, Math.max(0, times.steadyEnd - performance.now())),
        );
      }
      rtc.push(await stats());
    },
    async report() {
      return {
        parentTimeOrigin: performance.timeOrigin,
        audioBuffers:
          iframe.contentWindow?.moqAudioBuffers?.map((buffer) => ({
            positions: buffer.positions,
          })) ?? [],
        times,
        source: {
          width: 640,
          height: 360,
          requestedFps: 30,
          generated: Array.from(generated, ([id, at]) => ({ id, at })),
          trackSettings: track.getSettings(),
          audioState: audio.state,
        },
        moq,
        whep,
        rtc,
        final: await stats(),
        events,
        negotiations,
        received: { width: video.videoWidth, height: video.videoHeight },
        canvas: {
          width: iframe.contentDocument?.querySelector("canvas")?.width,
          height: iframe.contentDocument?.querySelector("canvas")?.height,
        },
      };
    },
    async stop() {
      stopped = true;
      clearInterval(paintTimer);
      cancelAnimationFrame(animation);
      video.cancelVideoFrameCallback(videoCallback);
      publisher.close();
      receiver.close();
      stream.getTracks().forEach((track) => track.stop());
      tone.stop();
      await audio.close();
      iframe.remove();
      video.remove();
      source.remove();
      viewers.remove();
    },
  };
}

declare global {
  interface Window {
    latencyExperiment: ReturnType<typeof createLatencyExperiment>;
  }
}
