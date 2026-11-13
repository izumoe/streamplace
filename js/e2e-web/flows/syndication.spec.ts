import { expect, test } from "@playwright/test";
import { readFileSync, writeFileSync } from "node:fs";

// The harness runs a second node that hears the test stream's origin record
// on the firehose and pulls its segments from the first node over Media over
// QUIC (see pkg/cmd/e2e.go and docs/moq-for-devs.md). Playback from that node is the
// whole transfer end to end: the origin advertising its MoQ URL, the peer
// dialling it with the pinned certificate, the MUXL segments arriving and
// validating, and the stream going live there.
//
// Web only: node-to-node transfer has no mobile surface.
test("syndication: stream plays from the node that pulls it over MoQ", async ({
  page,
}) => {
  const SERVER2_URL = process.env.SERVER2_URL;
  const did = process.env.ACCOUNT_DID;
  const logFile = process.env.E2E_LOGFILE;
  if (!SERVER2_URL || !did || !logFile) {
    throw new Error(
      "SERVER2_URL / ACCOUNT_DID / E2E_LOGFILE are not set — start the harness first",
    );
  }

  // The stream is live on the peer once its first pulled segment validates.
  await expect
    .poll(
      async () => {
        const res = await fetch(
          `${SERVER2_URL}/xrpc/place.stream.live.getLiveUsers?limit=50`,
        );
        if (!res.ok) return 0;
        const body = (await res.json()) as { streams?: unknown[] };
        return body.streams?.length ?? 0;
      },
      { timeout: 90_000, intervals: [2_000] },
    )
    .toBeGreaterThan(0);

  await page.addInitScript(() => {
    localStorage.setItem("player-transport", "hls");
  });
  await page.goto(`${SERVER2_URL}/${did}`);
  const video = page.locator("video").first();
  await expect(video).toBeVisible();
  // Frames are actually arriving from the peer, not just a player mounted.
  await expect
    .poll(
      () =>
        video.evaluate(
          (element: HTMLVideoElement) =>
            element.getVideoPlaybackQuality().totalVideoFrames,
        ),
      { timeout: 30_000 },
    )
    .toBeGreaterThan(0);
  // The peer would fall back to the origin's websocket if MoQ were broken,
  // so playback alone does not show which transport carried the stream.
  expect(readFileSync(logFile, "utf8")).toContain(
    "syndication: connected to origin (moq)",
  );
});

const replicationCase = process.env.E2E_REPLICATION_CASE ?? "aligned";
if (!["aligned", "unmatched"].includes(replicationCase)) {
  throw new Error(`Unknown replication case: ${replicationCase}`);
}

for (const scenario of ["aligned", "unmatched"] as const) {
  test(
    scenario === "aligned"
      ? "syndication: all AVC WebRTC rungs continue on the receiver"
      : "syndication: unmatched rendition does not interrupt the source",
    async ({ page }, testInfo) => {
      test.skip(
        !process.env.E2E_PEER_BINARY || process.env.E2E_ERTMP !== "1",
        "requires a peer binary and AVC fixtures for the selected scenario",
      );
      test.skip(replicationCase !== scenario);
      test.setTimeout(150_000);
      const server = process.env.SERVER2_URL!;
      const did = process.env.ACCOUNT_DID!;
      const version = await (await fetch(`${server}/xrpc/_health`)).json();
      expect(version.version).toBe(
        process.env.E2E_PEER_VERSION ?? "streamplace v0.13.4",
      );
      await page.goto(`${server}/api/healthz`);

      const primaryFps = scenario === "aligned" ? 30 : 60000 / 1001;
      const rungs = [
        { name: "source", width: 1920, height: 1080, fps: primaryFps },
        { name: "720p", width: 1280, height: 720, fps: primaryFps },
        { name: "360p", width: 640, height: 360, fps: 30 },
      ];
      const results = await page.evaluate(
        async ({ server, did, rungs, sampleSeconds }) => {
          return Promise.all(
            rungs.map(async (rung) => {
              const pc = new RTCPeerConnection({ bundlePolicy: "max-bundle" });
              const video = document.createElement("video");
              video.muted = true;
              video.autoplay = true;
              video.width = 320;
              document.body.append(video);
              let presented = 0;
              let mediaTime = 0;
              let callback = 0;
              const paint: VideoFrameRequestCallback = (_now, metadata) => {
                presented++;
                mediaTime = metadata.mediaTime;
                callback = video.requestVideoFrameCallback(paint);
              };
              callback = video.requestVideoFrameCallback(paint);
              const stream = new MediaStream();
              pc.ontrack = ({ track }) => {
                stream.addTrack(track);
                video.srcObject = stream;
                void video.play().catch(() => {});
              };
              try {
                pc.addTransceiver("video", { direction: "recvonly" });
                pc.addTransceiver("audio", { direction: "recvonly" });
                await pc.setLocalDescription(await pc.createOffer());
                await Promise.race([
                  new Promise<void>((resolve) => {
                    if (pc.iceGatheringState === "complete") resolve();
                    pc.addEventListener("icegatheringstatechange", () => {
                      if (pc.iceGatheringState === "complete") resolve();
                    });
                  }),
                  new Promise((resolve) => setTimeout(resolve, 2500)),
                ]);
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
                const started = performance.now();
                for (let i = 0; i < sampleSeconds; i++) {
                  await new Promise((resolve) => setTimeout(resolve, 1000));
                  const row = {
                    seconds: (performance.now() - started) / 1000,
                    frames: 0,
                    connectionState: pc.connectionState,
                    videoPackets: 0,
                    videoPacketsLost: 0,
                    audioPackets: 0,
                    audioConcealedSamples: 0,
                    presented,
                    mediaTime,
                    width: video.videoWidth,
                    height: video.videoHeight,
                  };
                  (await pc.getStats()).forEach((report) => {
                    if (report.type !== "inbound-rtp") return;
                    if (report.kind === "video") {
                      row.frames = report.framesDecoded ?? 0;
                      row.videoPackets = report.packetsReceived ?? 0;
                      row.videoPacketsLost = report.packetsLost ?? 0;
                    }
                    if (report.kind === "audio") {
                      row.audioPackets = report.packetsReceived ?? 0;
                      row.audioConcealedSamples = report.concealedSamples ?? 0;
                    }
                  });
                  samples.push(row);
                }
                return { rung, samples };
              } finally {
                video.cancelVideoFrameCallback(callback);
                pc.close();
                video.remove();
              }
            }),
          );
        },
        { server, did, rungs, sampleSeconds: scenario === "aligned" ? 70 : 40 },
      );
      const progressFile = testInfo.outputPath("rendition-progress.json");
      writeFileSync(progressFile, JSON.stringify(results));
      await testInfo.attach("receiver-rendition-progress", {
        path: progressFile,
        contentType: "application/json",
      });
      const log = readFileSync(process.env.E2E_LOGFILE!, "utf8");
      const connections = log
        .split("\n")
        .filter((line) => line.includes("syndication: connected to origin"));
      expect(connections.at(-1)).toBeDefined();
      const websocket = /^(1|t|true)$/i.test(
        process.env.E2E_WEBSOCKET_REPLICATION ?? "",
      );
      expect(connections.at(-1)?.includes("(moq)")).toBe(!websocket);
      if (scenario === "unmatched") {
        expect(connections).toHaveLength(1);
        if (!websocket) {
          expect(log).toContain("moq: rendition source audio did not arrive");
        }
      }
      for (const { rung, samples } of results) {
        if (scenario === "unmatched" && rung.name === "360p") continue;
        const steady = samples.filter((sample) => sample.seconds >= 10);
        for (let i = 10; i < steady.length; i++) {
          const before = steady[i - 10];
          const now = steady[i];
          const fps =
            (now.frames - before.frames) / (now.seconds - before.seconds);
          expect
            .soft([now.width, now.height], rung.name)
            .toEqual([rung.width, rung.height]);
          expect
            .soft(fps, `${rung.name}: decoder progress at ${now.seconds}s`)
            .toBeGreaterThan(rung.fps * 0.85);
          expect
            .soft(now.presented, `${rung.name}: painted frames`)
            .toBeGreaterThan(before.presented);
          expect
            .soft(now.mediaTime, `${rung.name}: painted media clock`)
            .toBeGreaterThan(before.mediaTime);
          expect
            .soft(now.audioPackets, `${rung.name}: audio progress`)
            .toBeGreaterThan(before.audioPackets);
        }
      }
    },
  );
}
