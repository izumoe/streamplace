import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { MoQPlayer } from "./moq-player";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

const SOURCE =
  "https://node.example/xrpc/place.stream.playback.getLivePlaylist?streamer=alice.example";

describe("MoQ player discovery lifecycle", () => {
  let container: HTMLDivElement;
  let root: Root;
  let canvasRef: { current: HTMLCanvasElement };
  let fallbackEvents: string[];

  function Discovery({ fallbackLabel }: { fallbackLabel: string }) {
    const [fallback, setFallback] = useState<string>();
    return (
      <>
        {!fallback && (
          <MoQPlayer
            canvasRef={canvasRef}
            src={SOURCE}
            paused={false}
            muted
            volume={1}
            onUnavailable={() => {
              fallbackEvents.push(fallbackLabel);
              setFallback(fallbackLabel);
            }}
            onPlaying={() => {}}
            onBufferingChange={() => {}}
            onQualitiesChange={() => {}}
            onCurrentQualityChange={() => {}}
            onStatsChange={() => {}}
          />
        )}
        <output>{fallback ?? "connecting"}</output>
      </>
    );
  }

  async function render(fallbackLabel = "WebRTC fallback") {
    await act(async () =>
      root.render(<Discovery fallbackLabel={fallbackLabel} />),
    );
  }

  beforeEach(() => {
    vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    canvasRef = { current: document.createElement("canvas") };
    fallbackEvents = [];
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
    vi.unstubAllGlobals();
    vi.useRealTimers();
  });

  it("falls back when the node does not advertise MoQ", async () => {
    vi.stubGlobal("fetch", async () => Response.json({ servers: [] }));
    await render();
    expect(container.textContent).toBe("WebRTC fallback");
  });

  it("cancels a pending discovery and never falls back after unmount", async () => {
    let signal: AbortSignal | undefined;
    vi.stubGlobal("fetch", async (_url: URL, options: RequestInit) => {
      signal = options.signal as AbortSignal;
      return new Promise((_resolve, reject) =>
        signal?.addEventListener("abort", () => reject(signal?.reason)),
      );
    });
    await render();
    await act(async () => root.unmount());
    expect(signal?.aborted).toBe(true);
    expect(fallbackEvents).toEqual([]);
    root = createRoot(container);
  });

  it("uses the latest callback without restarting discovery", async () => {
    let respond: (response: Response) => void = () => {};
    let signal: AbortSignal | undefined;
    let requests = 0;
    vi.stubGlobal("fetch", async (_url: URL, options: RequestInit) => {
      requests++;
      signal = options.signal as AbortSignal;
      return new Promise<Response>((resolve) => {
        respond = resolve;
      });
    });
    await render();
    await render("latest fallback");
    expect(requests).toBe(1);
    expect(signal?.aborted).toBe(false);

    await act(async () => respond(Response.json({ servers: [] })));
    expect(container.textContent).toBe("latest fallback");
    expect(fallbackEvents).toEqual(["latest fallback"]);
  });

  it("bounds startup when discovery never responds", async () => {
    vi.useFakeTimers();
    vi.stubGlobal(
      "fetch",
      async (_url: URL, options: RequestInit) =>
        new Promise((_resolve, reject) =>
          options.signal?.addEventListener("abort", () =>
            reject(options.signal?.reason),
          ),
        ),
    );
    await render();
    await act(async () => vi.advanceTimersByTimeAsync(10_000));
    expect(container.textContent).toBe("WebRTC fallback");
    await act(async () => vi.advanceTimersByTimeAsync(20_000));
    expect(fallbackEvents).toEqual(["WebRTC fallback"]);
  });
});
