import type * as Watch from "@moq/watch";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { MoQPlayer } from "./moq-player";
import type { PlayerStats } from "./player";

const fixture = vi.hoisted(() => ({
  player: undefined as unknown,
  audio: true as boolean | "empty",
  batchDurationMs: 1000,
  resolve: undefined as ((support: AudioDecoderSupport) => void) | undefined,
}));

type Playback = {
  sync: Watch.Sync;
  renderer: {
    out: {
      timestamp: Watch.Signals.Signal<Watch.Net.Time.Milli | undefined>;
      visible: Watch.Signals.Signal<boolean>;
    };
  };
  audio: {
    source: Watch.Audio.Source;
    out: { timestamp: Watch.Signals.Signal<Watch.Net.Time.Milli | undefined> };
  };
};

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

vi.mock("@moq/watch", async (original) => {
  const sdk = await original<typeof Watch>();
  const { Signal } = sdk.Signals;
  // Network, rendering and speakers are boundaries. Catalog propagation,
  // codec probing and rendition selection use the real SDK source and signals.
  class Player {
    broadcast = {
      out: {
        catalog: new Signal<Watch.Hang.Catalog.Root | undefined>(undefined),
      },
    };
    sync = new sdk.Sync();
    audio = {
      source: new sdk.Audio.Source({
        broadcast: this.broadcast as unknown as Watch.Broadcast,
        supported: sdk.Audio.Decoder.supported,
      }),
      out: {
        timestamp: new Signal<Watch.Net.Time.Milli | undefined>(undefined),
      },
    };
    video = {
      source: new sdk.Video.Source({
        broadcast: this.broadcast as unknown as Watch.Broadcast,
        supported: async () => true,
      }),
      out: {
        stats: new Signal(undefined),
        display: new Signal(undefined),
        buffered: new Signal([]),
        stalled: new Signal(false),
      },
    };
    renderer = {
      out: { timestamp: new Signal(undefined), visible: new Signal(false) },
    };

    constructor() {
      fixture.player = this;
      this.broadcast.out.catalog.set(
        sdk.Hang.Catalog.RootSchema.parse({
          streamplace: { batchDurationMs: fixture.batchDurationMs },
          ...(fixture.audio && {
            audio: {
              renditions:
                fixture.audio === "empty"
                  ? {}
                  : {
                      audio4: {
                        codec: "mp4a.40.2",
                        sampleRate: 48000,
                        numberOfChannels: 1,
                        description: "1188",
                        container: { kind: "cmaf", init: "" },
                      },
                    },
            },
          }),
        }),
      );
    }

    close() {
      this.audio.source.close();
      this.video.source.close();
      this.sync.close();
    }
  }

  return {
    ...sdk,
    Player,
    Net: {
      ...sdk.Net,
      Connection: class {
        error = new Signal(undefined);
        probe = new Signal(undefined);
        close() {}
      },
    },
  };
});

let container: HTMLDivElement;
let root: Root;
let unavailable = vi.fn<() => void>();
let stats: PlayerStats | undefined;

beforeEach(async () => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  const NativeURL = URL;
  vi.stubGlobal(
    "URL",
    class extends NativeURL {
      static createObjectURL() {
        return "blob:audio-worklet";
      }
    },
  );
  vi.stubGlobal("AudioDecoder", {
    isConfigSupported: () =>
      new Promise<AudioDecoderSupport>((resolve) => {
        fixture.resolve = resolve;
      }),
  });
  vi.stubGlobal("fetch", async () =>
    Response.json({
      moq: { url: "https://node.example", name: "playback/alice" },
    }),
  );
  stats = undefined;
  fixture.player = undefined;
  fixture.resolve = undefined;
  fixture.audio = true;
  fixture.batchDurationMs = 1000;
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
  unavailable = vi.fn<() => void>();
  await import("@moq/watch");
});

afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

async function render(muted = false) {
  await act(async () =>
    root.render(
      <MoQPlayer
        canvasRef={{ current: document.createElement("canvas") }}
        src="https://node.example/xrpc/place.stream.playback.getLivePlaylist?streamer=alice"
        paused={false}
        muted={muted}
        volume={1}
        onUnavailable={unavailable}
        onPlaying={() => {}}
        onBufferingChange={() => {}}
        onQualitiesChange={() => {}}
        onCurrentQualityChange={() => {}}
        onStatsChange={(value) => {
          stats = value;
        }}
      />,
    ),
  );
}

it("waits for an advertised audio codec's native support probe", async () => {
  await render();
  await expect.poll(() => fixture.resolve).toBeDefined();
  expect(unavailable).not.toHaveBeenCalled();
  await act(async () =>
    fixture.resolve!({
      supported: true,
      config: { codec: "mp4a.40.2", numberOfChannels: 1, sampleRate: 48000 },
    }),
  );
  expect(
    (fixture.player as Playback).audio.source.out.config.peek()?.codec,
  ).toBe("mp4a.40.2");
  expect(unavailable).not.toHaveBeenCalled();
});

it("bounds unmuted audio startup when a support probe never completes", async () => {
  vi.useFakeTimers({ toFake: ["Date", "setInterval", "clearInterval"] });
  await render();
  await act(async () => vi.advanceTimersByTimeAsync(9999));
  expect(unavailable).not.toHaveBeenCalled();
  await act(async () => vi.advanceTimersByTimeAsync(1));
  expect(unavailable).toHaveBeenCalledOnce();
});

it("falls back within the audio deadline when native decoding is unsupported", async () => {
  vi.useFakeTimers({ toFake: ["Date", "setInterval", "clearInterval"] });
  await render();
  const warning = vi.spyOn(console, "warn").mockImplementation(() => {});
  await act(async () =>
    fixture.resolve!({
      supported: false,
      config: { codec: "mp4a.40.2", numberOfChannels: 1, sampleRate: 48000 },
    }),
  );
  expect(warning.mock.calls).toEqual([
    [
      "no supported audio renditions found:",
      expect.objectContaining({ audio4: expect.any(Object) }),
    ],
  ]);
  await act(async () => vi.advanceTimersByTimeAsync(10_000));
  expect(unavailable).toHaveBeenCalledOnce();
  await act(async () => vi.advanceTimersByTimeAsync(10_000));
  expect(unavailable).toHaveBeenCalledOnce();
});

it.each(["muted audio", "video only", "empty audio catalog"])(
  "does not require audio progress for %s",
  async (kind) => {
    fixture.audio =
      kind === "video only"
        ? false
        : kind === "empty audio catalog"
          ? "empty"
          : true;
    vi.useFakeTimers({ toFake: ["Date", "setInterval", "clearInterval"] });
    await render(kind === "muted audio");
    await act(async () => vi.advanceTimersByTimeAsync(20_000));
    expect(unavailable).not.toHaveBeenCalled();
  },
);

it("reports MoQ receiver timing from the real sync clock, without inventing broadcaster latency", async () => {
  vi.useFakeTimers({ toFake: ["Date", "setInterval", "clearInterval"] });
  vi.spyOn(performance, "now").mockReturnValue(10_000);
  const sdk = await import("@moq/watch");
  await render(true);
  const player = fixture.player as Playback;
  await act(async () => {
    player.sync.received(sdk.Net.Time.Milli(6000));
    player.renderer.out.timestamp.set(sdk.Net.Time.Milli(4000));
    player.audio.out.timestamp.set(sdk.Net.Time.Milli(3900));
    await vi.advanceTimersByTimeAsync(1000);
  });
  expect(stats).toMatchObject({
    liveEdgeDelay: 2,
    playbackDelayMs: 1300,
    publicationBatchMs: 1000,
    avSyncMs: 100,
  });
  await act(async () => {
    player.audio.out.timestamp.set(sdk.Net.Time.Milli(4100));
    await vi.advanceTimersByTimeAsync(1000);
  });
  expect(stats?.avSyncMs).toBe(-100);
  await act(async () => {
    player.renderer.out.timestamp.set(undefined);
    await vi.advanceTimersByTimeAsync(1000);
  });
  expect(stats?.liveEdgeDelay).toBeUndefined();
  expect(stats?.avSyncMs).toBeUndefined();
});

it("allows the requested playout buffer before declaring first-frame startup stalled", async () => {
  fixture.audio = false;
  vi.useFakeTimers({ toFake: ["Date", "setInterval", "clearInterval"] });
  const sdk = await import("@moq/watch");
  await render(true);
  const player = fixture.player as Playback;
  await act(async () => {
    player.renderer.out.visible.set(true);
    await vi.advanceTimersByTimeAsync(3000);
  });
  expect(player.sync.out.delay.peek()).toBe(1300);
  expect(unavailable).not.toHaveBeenCalled();
  await act(async () => {
    player.renderer.out.timestamp.set(sdk.Net.Time.Milli(0));
    await vi.advanceTimersByTimeAsync(1000);
  });
  expect(unavailable).not.toHaveBeenCalled();
});

it.each([
  [1000, 4000],
  [9000, 10_000],
])(
  "bounds first-frame startup for a %i ms publication batch",
  async (batch, deadline) => {
    fixture.audio = false;
    fixture.batchDurationMs = batch;
    vi.useFakeTimers({ toFake: ["Date", "setInterval", "clearInterval"] });
    await render(true);
    await act(async () => {
      (fixture.player as Playback).renderer.out.visible.set(true);
      await vi.advanceTimersByTimeAsync(deadline - 1);
    });
    expect(unavailable).not.toHaveBeenCalled();
    await act(async () => vi.advanceTimersByTimeAsync(1));
    expect(unavailable).toHaveBeenCalledOnce();
  },
);

it("keeps the ten-second stall allowance after the first rendered frame disappears", async () => {
  fixture.audio = false;
  vi.useFakeTimers({ toFake: ["Date", "setInterval", "clearInterval"] });
  const sdk = await import("@moq/watch");
  await render(true);
  const player = fixture.player as Playback;
  await act(async () => {
    player.renderer.out.visible.set(true);
    player.renderer.out.timestamp.set(sdk.Net.Time.Milli(0));
    await vi.advanceTimersByTimeAsync(1000);
    player.renderer.out.timestamp.set(undefined);
    await vi.advanceTimersByTimeAsync(9999);
  });
  expect(unavailable).not.toHaveBeenCalled();
  await act(async () => vi.advanceTimersByTimeAsync(1));
  expect(unavailable).toHaveBeenCalledOnce();
});
