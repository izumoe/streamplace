// @vitest-environment node
import { type Broadcast, Hang, Signals, Video } from "@moq/watch";
import { expect, it } from "vitest";

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
  } finally {
    source.close();
  }
});
