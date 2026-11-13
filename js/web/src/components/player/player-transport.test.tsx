import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, expect, it, vi } from "vitest";
import { FullscreenProvider } from "../../contexts/fullscreen-context";
import * as MoQBackend from "./moq-player";
import { Player, type PlayerStats } from "./player";

const { importWarning } = vi.hoisted(() => ({
  importWarning: vi.spyOn(console, "warn").mockImplementation(() => {}),
}));

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

beforeAll(() => {
  // Node 22 uses the dependency's supported base64 fallback during import.
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

beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  vi.stubGlobal("matchMedia", (media: string) => ({
    media,
    matches: false,
    addEventListener() {},
    removeEventListener() {},
  }));
  vi.stubGlobal(
    "RTCPeerConnection",
    class {
      addTransceiver() {}
      addEventListener() {}
      getStats() {
        return Promise.resolve(new Map());
      }
      close() {}
    },
  );
  vi.spyOn(HTMLMediaElement.prototype, "canPlayType").mockReturnValue(
    "probably",
  );
  vi.spyOn(HTMLMediaElement.prototype, "load").mockImplementation(() => {});
});

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  localStorage.clear();
});

it("uses the VOD playlist and start position even with a live transport preference", async () => {
  localStorage.setItem("player-transport", "webrtc");
  const src = "https://node.example/vod/playlist.m3u8";
  const container = document.createElement("div");
  document.body.appendChild(container);
  const root = createRoot(container);
  try {
    await act(async () =>
      root.render(
        <FullscreenProvider>
          <Player src={src} mode="vod" active startTime={12} />
        </FullscreenProvider>,
      ),
    );
    const video = container.querySelector("video")!;
    expect(video.src).toBe(src);
    Object.defineProperty(video, "duration", { configurable: true, value: 60 });
    await act(async () => video.dispatchEvent(new Event("loadedmetadata")));
    expect(video.currentTime).toBe(12);
  } finally {
    await act(async () => root.unmount());
    container.remove();
  }
});

it("keeps an explicit WebRTC source on WebRTC without discovery requests", async () => {
  vi.stubGlobal("isSecureContext", true);
  for (const api of [
    "WebTransport",
    "VideoDecoder",
    "AudioDecoder",
    "AudioContext",
  ]) {
    vi.stubGlobal(api, class {});
  }
  const requests: URL[] = [];
  vi.stubGlobal("fetch", async (url: URL) => {
    requests.push(url);
    return Response.json({ servers: [] });
  });
  const container = document.createElement("div");
  document.body.appendChild(container);
  const root = createRoot(container);
  try {
    await act(async () =>
      root.render(
        <FullscreenProvider>
          <Player src="webrtc://node.example/?streamer=did:plc:alice" active />
        </FullscreenProvider>,
      ),
    );
    expect(
      container
        .querySelector("[data-playback-transport]")
        ?.getAttribute("data-playback-transport"),
    ).toBe("webrtc");
    expect(requests).toEqual([]);
  } finally {
    await act(async () => root.unmount());
    container.remove();
  }
});

it("falls back from failed WebRTC without saving it as the viewer's preference", async () => {
  localStorage.setItem("player-transport", "webrtc");
  const container = document.createElement("div");
  document.body.appendChild(container);
  const root = createRoot(container);
  try {
    await act(async () =>
      root.render(
        <FullscreenProvider>
          <Player
            src="https://node.example/playlist?streamer=did:plc:alice"
            active
          />
        </FullscreenProvider>,
      ),
    );
    const video = container.querySelector("video")!;
    Object.defineProperty(video, "error", { value: { code: 2 } });
    await act(async () => video.dispatchEvent(new Event("error")));
    expect(
      container
        .querySelector("[data-playback-transport]")
        ?.getAttribute("data-playback-transport"),
    ).toBe("hls");
    expect(localStorage.getItem("player-transport")).toBe("webrtc");
  } finally {
    await act(async () => root.unmount());
    container.remove();
  }
});

it("keeps the viewer unmuted when switching from MoQ to standard playback", async () => {
  vi.stubGlobal("isSecureContext", true);
  for (const api of [
    "WebTransport",
    "VideoDecoder",
    "AudioDecoder",
    "AudioContext",
  ]) {
    vi.stubGlobal(api, class {});
  }
  vi.stubGlobal(
    "fetch",
    async (_url: URL, options: RequestInit) =>
      new Promise((_resolve, reject) =>
        options.signal?.addEventListener("abort", () =>
          reject(options.signal?.reason),
        ),
      ),
  );
  const container = document.createElement("div");
  document.body.appendChild(container);
  const root = createRoot(container);
  try {
    await act(async () =>
      root.render(
        <FullscreenProvider>
          <Player
            src="https://node.example/playlist?streamer=did:plc:alice"
            active
          />
        </FullscreenProvider>,
      ),
    );
    await act(async () =>
      container
        .querySelector<HTMLButtonElement>('button[title="player-unmute"]')!
        .click(),
    );
    await act(async () =>
      container
        .querySelector<HTMLButtonElement>('button[title="player-settings"]')!
        .click(),
    );
    const standard = Array.from(
      document.querySelectorAll<HTMLElement>('[role="menuitemradio"]'),
    ).find((item) => item.textContent === "player-latency-standard")!;
    await act(async () => standard.click());
    expect(
      container
        .querySelector("[data-playback-transport]")
        ?.getAttribute("data-playback-transport"),
    ).toBe("hls");
    expect(container.querySelector("video")!.muted).toBe(false);
    expect(localStorage.getItem("player-transport")).toBe("hls");
  } finally {
    await act(async () => root.unmount());
    container.remove();
  }
});

it.each([undefined, 0, 7])(
  "shows optional statistics only when the backend reports them (%s)",
  async (value) => {
    vi.stubGlobal("isSecureContext", true);
    for (const api of [
      "WebTransport",
      "VideoDecoder",
      "AudioDecoder",
      "AudioContext",
    ]) {
      vi.stubGlobal(api, class {});
    }
    let reportStats: (stats: PlayerStats) => void = () => {};
    vi.spyOn(MoQBackend, "MoQPlayer").mockImplementation(
      ({ onStatsChange }) => {
        reportStats = onStatsChange;
        return null;
      },
    );
    const container = document.createElement("div");
    document.body.appendChild(container);
    const root = createRoot(container);
    try {
      await act(async () =>
        root.render(
          <FullscreenProvider>
            <Player
              src="https://node.example/playlist?streamer=did:plc:alice"
              active
            />
          </FullscreenProvider>,
        ),
      );
      await act(async () =>
        reportStats({
          width: 320,
          height: 180,
          viewportWidth: 1280,
          viewportHeight: 720,
          buffered: 1,
          droppedFrames: value,
          fps: value,
          bitrate: value === undefined ? undefined : value * 1000,
          renditionBitrate: value === undefined ? undefined : value * 1000,
          availableBandwidth: value === undefined ? undefined : value * 2000,
          liveEdgeDelay: value,
          playbackDelayMs: value,
          publicationBatchMs: value,
          roundTripTimeMs: value,
          avSyncMs: value === undefined ? undefined : -value,
          processingDelayMs: value,
          decodeTimeMs: value,
          jitterBufferMs: value,
          totalFrames: 120,
          level: -1,
        }),
      );
      await act(async () =>
        container
          .querySelector<HTMLButtonElement>('button[title="player-settings"]')!
          .click(),
      );
      const toggle = Array.from(
        document.querySelectorAll<HTMLElement>('[role="menuitemcheckbox"]'),
      ).find((item) => item.textContent === "player-stats")!;
      await act(async () => toggle.click());
      await act(async () => {
        await vi.dynamicImportSettled();
      });
      const summary = container.querySelector(
        '[role="dialog"][aria-label="player-stats"]',
      )!;
      expect(summary).not.toBeNull();
      const dialog = summary;
      const text = dialog.textContent ?? "";
      expect(dialog.textContent?.includes("player-stats-skipped")).toBe(
        value !== undefined,
      );
      expect(dialog.textContent?.includes("FPS")).toBe(value !== undefined);
      for (const label of [
        "bitrate",
        "rendition-bitrate",
        "available-bandwidth",
        "live-edge-delay",
        "playback-delay",
        "publication-batch",
        "rtt",
        "av-sync",
        "processing-delay",
        "decode-time",
        "jitter-buffer",
      ]) {
        expect(text.includes(`player-stats-${label}`)).toBe(
          value !== undefined,
        );
      }
      expect(dialog.textContent).toContain("player-stats-broadcaster-delay");
      expect(dialog.textContent).toContain("player-stats-not-measured");
      expect(dialog.querySelector("details")).toBeNull();
      expect(dialog.textContent).toContain("player-stats-session");
      if (value !== undefined) {
        expect(text).toContain(`${value.toFixed(1)} ms`);
        expect(text).toContain(`${(-value).toFixed(1)} ms`);
        expect(dialog.textContent).toContain(`${value} / 120`);
      }
    } finally {
      await act(async () => root.unmount());
      container.remove();
    }
  },
);
