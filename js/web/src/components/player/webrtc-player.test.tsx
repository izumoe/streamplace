import { createServer } from "node:http";
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

class TestStream {
  tracks: MediaStreamTrack[] = [];
  addTrack(track: MediaStreamTrack) {
    this.tracks.push(track);
  }
  removeTrack(track: MediaStreamTrack) {
    this.tracks = this.tracks.filter((item) => item !== track);
  }
  getVideoTracks() {
    return this.tracks.filter((track) => track.kind === "video");
  }
  getAudioTracks() {
    return this.tracks.filter((track) => track.kind === "audio");
  }
}

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
  vi.stubGlobal("MediaStream", TestStream);
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

it("resolves an early random audio ID after the answer is available", async () => {
  await act(async () => root.unmount());
  const answer =
    "m=audio 9 UDP/TLS/RTP/SAVPF 111\r\na=mid:main\r\na=msid:pion audio\r\n";
  const server = createServer((_request, response) => {
    response.setHeader("Content-Type", "application/sdp");
    response.setHeader("Access-Control-Allow-Origin", "*");
    response.end(answer);
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  const address = server.address();
  if (!address || typeof address === "string")
    throw new Error("WHEP fixture did not bind");
  const video = document.createElement("video");
  let earlyAudio: MediaStreamTrack[] = [];
  const main = Object.assign(new EventTarget(), {
    id: "browser-id",
    kind: "audio",
    readyState: "live",
  });
  class LatePeer extends Peer {
    iceGatheringState = "complete";
    signalingState = "stable";
    localDescription: RTCSessionDescriptionInit | null = null;
    remoteDescription: RTCSessionDescriptionInit | null = null;
    negotiating = false;
    addTransceiver() {
      if (this.negotiating) return;
      this.negotiating = true;
      queueMicrotask(() => this.dispatchEvent(new Event("negotiationneeded")));
    }
    async createOffer() {
      return { type: "offer", sdp: "v=0" };
    }
    async setLocalDescription(description: RTCSessionDescriptionInit) {
      this.localDescription = description;
    }
    async setRemoteDescription(description: RTCSessionDescriptionInit) {
      this.dispatchEvent(
        Object.assign(new Event("track"), {
          track: main,
          transceiver: { mid: "main" },
          streams: [],
        }),
      );
      earlyAudio = (video.srcObject as unknown as TestStream).getAudioTracks();
      this.remoteDescription = description;
      this.dispatchEvent(new Event("signalingstatechange"));
    }
  }
  vi.stubGlobal("RTCPeerConnection", LatePeer);
  vi.stubGlobal(
    "RTCSessionDescription",
    class {
      constructor(value: RTCSessionDescriptionInit) {
        Object.assign(this, value);
      }
    },
  );
  const play = vi
    .spyOn(HTMLMediaElement.prototype, "play")
    .mockResolvedValue(undefined);
  root = createRoot(container);
  try {
    await act(async () =>
      root.render(
        <WebRTCPlayer
          videoRef={{ current: video }}
          src={`http://127.0.0.1:${address.port}/playlist?streamer=did:plc:test`}
          active
        />,
      ),
    );
    await act(async () =>
      vi.waitFor(() =>
        expect(
          (video.srcObject as unknown as TestStream | null)?.getAudioTracks(),
        ).toEqual([main]),
      ),
    );
    expect(earlyAudio).toEqual([]);
    expect(main.readyState).toBe("live");
  } finally {
    await act(async () => root.unmount());
    play.mockRestore();
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
  expect(video.srcObject).toBeNull();
  expect(Peer.peers.at(-1)!.connectionState).toBe("closed");
});
