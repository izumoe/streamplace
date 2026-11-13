// @vitest-environment node
import { type Broadcast, Hang, Player, Signals, Video } from "@moq/watch";
import { expect, it, vi } from "vitest";

it("keeps the best supported resolution when a probe arrives without rendition bitrates", async () => {
  const catalog = new Signals.Signal(
    Hang.Catalog.RootSchema.parse({
      video: {
        renditions: {
          video1: {
            codec: "avc1.64001e",
            codedWidth: 426,
            codedHeight: 240,
            container: { kind: "legacy" },
          },
          video3: {
            codec: "av01.0.04M.08",
            codedWidth: 1280,
            codedHeight: 720,
            container: { kind: "legacy" },
          },
        },
      },
    }),
  );
  const probe = new Signals.Signal<{ estimatedRecvRate: number } | undefined>(
    undefined,
  );
  // The network boundary supplies a catalog; selection and signals are the
  // installed SDK, so this also verifies that its package patch is applied.
  const source = new Video.Source({
    broadcast: { out: { catalog } } as unknown as Broadcast,
    supported: async () => true,
    probe,
  });
  try {
    await expect.poll(() => source.out.track.peek()).toBe("video3");
    probe.set({ estimatedRecvRate: 5_000_000 });
    // Signal writes coalesce in a microtask, including unchanged selections.
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(source.out.track.peek()).toBe("video3");
    const current = catalog.peek();
    catalog.set(
      Hang.Catalog.RootSchema.parse({
        ...current,
        video: {
          renditions: {
            video1: { ...current.video?.renditions.video1, bitrate: 2_000_000 },
            video3: { ...current.video?.renditions.video3, bitrate: 1_000_000 },
          },
        },
      }),
    );
    await expect.poll(() => source.out.config.peek()?.bitrate).toBe(1_000_000);
    expect(source.out.track.peek()).toBe("video3");
  } finally {
    source.close();
  }
});

it("reserves audio in the installed player's automatic video budget", async () => {
  const codec = {
    async isConfigSupported() {
      return { supported: true };
    },
  };
  class Context extends EventTarget {
    state = "running";
    audioWorklet = { addModule: () => new Promise<void>(() => {}) };
    async close() {}
  }
  // A manual catalog exercises the actual selectors without opening a network
  // subscription. Browser resources stop at capability probes and worklet load.
  vi.stubGlobal("VideoDecoder", codec);
  vi.stubGlobal("AudioDecoder", codec);
  vi.stubGlobal("AudioContext", Context);
  const catalog = Hang.Catalog.RootSchema.parse({
    video: {
      renditions: {
        low: {
          codec: "avc1.64001e",
          bitrate: 500_000,
          codedWidth: 426,
          codedHeight: 240,
          container: { kind: "legacy" },
        },
        high: {
          codec: "avc1.640028",
          bitrate: 1_500_000,
          codedWidth: 1280,
          codedHeight: 720,
          container: { kind: "legacy" },
        },
      },
    },
    audio: {
      renditions: {
        audio: {
          codec: "opus",
          sampleRate: 48_000,
          numberOfChannels: 2,
          bitrate: 200_000,
          container: { kind: "legacy" },
        },
      },
    },
  });
  const muted = new Signals.Signal(false);
  const probe = new Signals.Signal({ estimatedRecvRate: 2_000_000 });
  const player = new Player({
    catalogFormat: "manual",
    catalog,
    probe,
    muted,
    visible: "always",
  });
  try {
    await expect
      .poll(() => player.audio.source.out.config.peek()?.bitrate)
      .toBe(200_000);
    await expect.poll(() => player.video.source.out.track.peek()).toBe("low");
    muted.set(true);
    await expect.poll(() => player.video.source.out.track.peek()).toBe("high");
    muted.set(false);
    probe.set({ estimatedRecvRate: 100_000 });
    await expect
      .poll(() => player.video.source.in.target.peek()?.bitrate)
      .toBe(0);
    await expect.poll(() => player.video.source.out.track.peek()).toBe("low");
  } finally {
    player.close();
    vi.unstubAllGlobals();
  }
});
