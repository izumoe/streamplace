import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, expect, it, vi } from "vitest";
import { WebRTCPlayer } from "./webrtc-player";

const { importWarning } = vi.hoisted(() => ({
  importWarning: vi.spyOn(console, "warn").mockImplementation(() => {}),
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

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

// This browser boundary controls connection loss and asynchronous stats delivery.
// Negotiation is covered by the real WebRTC browser flow.
class Peer extends EventTarget {
  static peers: Peer[] = [];
  connectionState = "connected";
  getStats = () => Promise.resolve(new Map());
  constructor() {
    super();
    Peer.peers.push(this);
  }
  addTransceiver() {}
  close() {
    this.connectionState = "closed";
    this.dispatchEvent(new Event("connectionstatechange"));
  }
}

let root: Root;
let container: HTMLDivElement;
const snapshots: unknown[] = [];

beforeEach(async () => {
  vi.useFakeTimers();
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  vi.stubGlobal("RTCPeerConnection", Peer);
  Peer.peers = [];
  snapshots.length = 0;
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () =>
    root.render(
      <WebRTCPlayer
        videoRef={{ current: document.createElement("video") }}
        src="https://example.test/live/stream.m3u8?streamer=test"
        active
        onStatsChange={(stats) => snapshots.push(stats)}
      />,
    ),
  );
});

afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

it("keeps one stats poll across repeated reconnects and removes it on unmount", async () => {
  expect(vi.getTimerCount()).toBe(1);
  for (let attempt = 0; attempt < 3; attempt++) {
    await act(async () => {
      Peer.peers.at(-1)!.close();
      await vi.advanceTimersByTimeAsync(3000);
    });
    expect(Peer.peers).toHaveLength(attempt + 2);
    expect(vi.getTimerCount()).toBe(1);
  }
  await act(async () => root.unmount());
  expect(vi.getTimerCount()).toBe(0);
});

it.each(["reconnect", "unmount"])(
  "discards a stats result that arrives after %s",
  async (action) => {
    let resolve!: (stats: Map<string, unknown>) => void;
    Peer.peers[0].getStats = () =>
      new Promise<Map<string, unknown>>((done) => {
        resolve = done;
      });
    await act(async () => vi.advanceTimersByTimeAsync(1000));
    if (action === "reconnect") {
      await act(async () => {
        Peer.peers[0].close();
        await vi.advanceTimersByTimeAsync(3000);
      });
    } else {
      await act(async () => root.unmount());
    }
    expect(snapshots).toHaveLength(0);
    await act(async () => resolve(new Map()));
    expect(snapshots).toHaveLength(0);
  },
);
