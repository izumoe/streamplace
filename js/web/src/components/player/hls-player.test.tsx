import { createServer, type Server } from "node:http";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import {
  afterAll,
  afterEach,
  beforeAll,
  beforeEach,
  expect,
  it,
  vi,
} from "vitest";
import { HLSPlayer } from "./hls-player";
import type { PlayerStats, QualityOption } from "./player";

const { importWarning } = vi.hoisted(() => {
  vi.stubEnv("DEV", false);
  return {
    importWarning: vi.spyOn(console, "warn").mockImplementation(() => {}),
  };
});

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

beforeAll(() => {
  try {
    expect(importWarning.mock.calls).toEqual([
      [
        "[@atproto/lex-data]: Uint8Array.fromBase64 / Uint8Array.prototype.toBase64 not available in this environment. Falling back to ponyfill implementation.",
      ],
    ]);
  } finally {
    importWarning.mockRestore();
  }
});

afterAll(() => vi.unstubAllEnvs());

let server: Server;
let root: Root;
let container: HTMLDivElement;
let origin: string;
let liveManifest = false;

beforeEach(async () => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  // The unit exercises hls.js's real loader and level parser. The jsdom
  // boundary supplies codec support and media attachment, not decode.
  vi.stubGlobal(
    "MediaSource",
    class extends EventTarget {
      static isTypeSupported() {
        return true;
      }
      readyState = "closed";
      sourceBuffers = [];
    },
  );
  const NativeURL = URL;
  vi.stubGlobal(
    "URL",
    class extends NativeURL {
      static createObjectURL(source: MediaSource) {
        if (liveManifest)
          queueMicrotask(() => {
            Object.defineProperty(source, "readyState", {
              value: "open",
              configurable: true,
            });
            source.dispatchEvent(new Event("sourceopen"));
          });
        return "blob:hls-quality-test";
      }
      static revokeObjectURL() {}
    },
  );
  vi.spyOn(HTMLMediaElement.prototype, "play").mockResolvedValue(undefined);
  vi.spyOn(HTMLMediaElement.prototype, "load").mockImplementation(() => {});
  liveManifest = false;
  server = createServer((request, response) => {
    response.setHeader("Access-Control-Allow-Origin", "*");
    response.setHeader("Content-Type", "application/vnd.apple.mpegurl");
    const url = new URL(request.url!, `http://${request.headers.host}`);
    if (url.searchParams.has("track")) {
      response.end(
        liveManifest
          ? "#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:0\n#EXTINF:2,\n0.ts\n#EXTINF:2,\n1.ts\n#EXTINF:2,\n2.ts\n"
          : "#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-ENDLIST\n",
      );
      return;
    }
    const variant =
      '#EXT-X-STREAM-INF:BANDWIDTH=120000,CODECS="avc1.42E01E",RESOLUTION=160x120\n';
    response.end(
      `#EXTM3U\n${variant}${url.origin}${url.pathname}?track=1\n${variant}${url.origin}${url.pathname}?track=2\n`,
    );
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  const address = server.address();
  if (!address || typeof address === "string")
    throw new Error("HTTP fixture did not bind");
  origin = `http://127.0.0.1:${address.port}`;
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
});

afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
  await new Promise<void>((resolve) => server.close(() => resolve()));
  localStorage.clear();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

it.each([
  {
    title:
      "keeps both equal-bandwidth signed tracks in our live manifest selectable",
    path: "/xrpc/place.stream.playback.getLivePlaylist?streamer=did:plc:streamer",
    labels: ["120p (H.264, video1)", "120p (H.264, video2)"],
  },
  {
    title:
      "preserves ordinary redundant-pathway handling for external manifests",
    path: "/external.m3u8",
    labels: ["120p"],
  },
])("$title", async ({ path, labels }) => {
  let qualities: QualityOption[] | undefined;
  const videoRef = { current: document.createElement("video") };
  await act(async () =>
    root.render(
      <HLSPlayer
        active
        videoRef={videoRef}
        src={origin + path}
        onQualitiesChange={(value) => {
          qualities = value;
        }}
      />,
    ),
  );
  await expect.poll(() => qualities).toBeDefined();
  expect(qualities).toEqual([
    { index: -1, label: "player-quality-auto" },
    ...labels.map((label, index) => ({ index, label })),
  ]);
});

it("reports distance to the HLS live edge rather than distance to its delayed sync target", async () => {
  liveManifest = true;
  let stats: PlayerStats | undefined;
  const video = document.createElement("video");
  video.currentTime = 1;
  await act(async () =>
    root.render(
      <HLSPlayer
        active
        videoRef={{ current: video }}
        src={origin + "/external.m3u8"}
        onStatsChange={(value) => {
          stats = value;
        }}
      />,
    ),
  );
  await expect
    .poll(
      () => {
        video.dispatchEvent(new Event("timeupdate"));
        return stats?.liveEdgeDelay;
      },
      { timeout: 3000 },
    )
    .toBeGreaterThanOrEqual(5);
  expect(stats?.playbackDelayMs).toBe(10_000);
});
