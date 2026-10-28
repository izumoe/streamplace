import { afterEach, describe, expect, it, vi } from "vitest";
import {
  getMoqPlaybackFloor,
  getMoqPlaybackSource,
  supportsMoqPlayback,
} from "./moq-source";

const SOURCE =
  "https://node.example/xrpc/place.stream.playback.getLivePlaylist?streamer=alice.example";
const FINGERPRINT = "ab".repeat(32);

afterEach(() => vi.unstubAllGlobals());

describe("MoQ playback floor", () => {
  it.each([1, 1001, 9799])(
    "reserves receiver scheduling time for a %s millisecond batch",
    (batchDurationMs) => {
      expect(getMoqPlaybackFloor({ streamplace: { batchDurationMs } })).toBe(
        batchDurationMs + 200,
      );
    },
  );

  it("consumes the allocated reserve before increasing the playback floor", () => {
    let floor: number | undefined;
    const floors = [362, 375, 562, 563].map((batchDurationMs) => {
      floor = getMoqPlaybackFloor({ streamplace: { batchDurationMs } }, floor);
      return floor;
    });
    expect(floors).toEqual([562, 562, 562, 763]);
  });

  it("starts with a fresh reserve for a new broadcast", () => {
    expect(getMoqPlaybackFloor({ streamplace: { batchDurationMs: 400 } })).toBe(
      600,
    );
    expect(getMoqPlaybackFloor({ streamplace: { batchDurationMs: 100 } })).toBe(
      300,
    );
  });

  it("keeps valid allocations within the playback budget", () => {
    expect(
      getMoqPlaybackFloor({ streamplace: { batchDurationMs: 9999 } }, 9999),
    ).toBe(9999);
    expect(
      getMoqPlaybackFloor({ streamplace: { batchDurationMs: 9800 } }, 9799),
    ).toBeUndefined();
    expect(
      getMoqPlaybackFloor({ streamplace: { batchDurationMs: 10_000 } }, 9999),
    ).toBeUndefined();
    expect(getMoqPlaybackFloor({}, 9999)).toBeUndefined();
  });

  it.each([
    {},
    { streamplace: null },
    { streamplace: "timing" },
    { streamplace: {} },
    ...[
      0,
      -1,
      0.5,
      9800,
      9999,
      10_000,
      Number.MAX_SAFE_INTEGER,
      Infinity,
      NaN,
      "1000",
    ].map((batchDurationMs) => ({ streamplace: { batchDurationMs } })),
  ])(
    "refuses timing that cannot fit the playback progress budget: %j",
    (catalog) => {
      expect(getMoqPlaybackFloor(catalog)).toBeUndefined();
    },
  );
});

describe("supportsMoqPlayback", () => {
  const APIs = ["WebTransport", "VideoDecoder", "AudioDecoder", "AudioContext"];

  it("requires a secure context and the browser media APIs", () => {
    vi.stubGlobal("isSecureContext", true);
    for (const api of APIs) vi.stubGlobal(api, class {});
    expect(supportsMoqPlayback()).toBe(true);
    vi.stubGlobal("isSecureContext", false);
    expect(supportsMoqPlayback()).toBe(false);
  });

  it.each(APIs)("falls back when %s is unavailable", (missing) => {
    vi.stubGlobal("isSecureContext", true);
    for (const api of APIs) vi.stubGlobal(api, class {});
    vi.stubGlobal(missing, undefined);
    expect(supportsMoqPlayback()).toBe(false);
  });
});

describe("getMoqPlaybackSource", () => {
  it("discovers the resolved broadcast and pins the advertised certificate", async () => {
    const requests: URL[] = [];
    vi.stubGlobal("fetch", async (url: URL) => {
      requests.push(url);
      return Response.json({
        servers: ["https://node.example"],
        moq: {
          url: `moqt://media.example:38443/?certhash=${FINGERPRINT}`,
          name: "did:plc:alice",
        },
      });
    });

    const result = await getMoqPlaybackSource(
      SOURCE,
      new AbortController().signal,
    );

    expect(requests[0].origin).toBe("https://node.example");
    expect(requests[0].pathname).toBe(
      "/xrpc/place.stream.playback.getPlaybackServer",
    );
    expect(requests[0].searchParams.get("stream")).toBe("alice.example");
    expect(result).toEqual({
      url: new URL("https://media.example:38443/"),
      name: "did:plc:alice",
      certificateHash: FINGERPRINT,
    });
  });

  it("accepts a public TLS endpoint without a certificate pin", async () => {
    vi.stubGlobal("fetch", async () =>
      Response.json({
        moq: { url: "https://media.example:4433/moq", name: "did:plc:alice" },
      }),
    );
    expect(
      await getMoqPlaybackSource(SOURCE, new AbortController().signal),
    ).toEqual({
      url: new URL("https://media.example:4433/moq"),
      name: "did:plc:alice",
      certificateHash: undefined,
    });
  });

  it("lets older nodes use existing playback transports", async () => {
    vi.stubGlobal("fetch", async () =>
      Response.json({ servers: ["https://node.example"] }),
    );
    expect(
      await getMoqPlaybackSource(SOURCE, new AbortController().signal),
    ).toBeUndefined();
  });

  it("does not discover MoQ for a source without a streamer", async () => {
    vi.stubGlobal("fetch", () => {
      throw new Error("unexpected discovery request");
    });
    expect(
      await getMoqPlaybackSource(
        "https://node.example/video.mp4",
        new AbortController().signal,
      ),
    ).toBeUndefined();
  });

  it.each([
    { url: "http://media.example:38443/", name: "did:plc:alice" },
    { url: "moqt://media.example/?certhash=abc", name: "did:plc:alice" },
    { url: "moqt://media.example/?certhash=", name: "did:plc:alice" },
    {
      url: `moqt://media.example/?certhash=${"zz".repeat(32)}`,
      name: "did:plc:alice",
    },
    { url: "moqt://media.example/", name: "" },
    { url: "moqt://media.example/" },
    { url: 42, name: "did:plc:alice" },
  ])("rejects an unusable advertisement: %j", async (moq) => {
    vi.stubGlobal("fetch", async () => Response.json({ moq }));
    await expect(
      getMoqPlaybackSource(SOURCE, new AbortController().signal),
    ).rejects.toThrow();
  });

  it("propagates discovery failures so playback can fall back", async () => {
    vi.stubGlobal("fetch", async () => new Response(null, { status: 503 }));
    await expect(
      getMoqPlaybackSource(SOURCE, new AbortController().signal),
    ).rejects.toThrow("503");
  });

  it("cancels discovery when the player leaves the stream", async () => {
    const controller = new AbortController();
    vi.stubGlobal("fetch", async (_url: URL, options: RequestInit) => {
      expect(options.signal).toBe(controller.signal);
      return new Promise((_resolve, reject) => {
        options.signal?.addEventListener("abort", () =>
          reject(controller.signal.reason),
        );
      });
    });
    const result = getMoqPlaybackSource(SOURCE, controller.signal);
    controller.abort();
    await expect(result).rejects.toMatchObject({ name: "AbortError" });
  });
});
