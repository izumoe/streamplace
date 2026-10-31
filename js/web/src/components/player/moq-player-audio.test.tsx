import type * as Watch from "@moq/watch";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { MoQPlayer } from "./moq-player";

const fixture = vi.hoisted(() => ({
  player: undefined as unknown,
  audio: true as boolean | "empty",
  resolve: undefined as ((support: AudioDecoderSupport) => void) | undefined,
}));

type Playback = {
  audio: {
    source: Watch.Audio.Source;
    out: { timestamp: Watch.Signals.Signal<Watch.Net.Time.Micro | undefined> };
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
        timestamp: new Signal<Watch.Net.Time.Micro | undefined>(undefined),
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
          streamplace: { batchDurationMs: 1000 },
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
  fixture.player = undefined;
  fixture.resolve = undefined;
  fixture.audio = true;
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
        onStatsChange={() => {}}
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
