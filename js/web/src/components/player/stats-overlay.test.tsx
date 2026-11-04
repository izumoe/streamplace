import { act, Profiler } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { PlayerStats } from "./player";
import StatsOverlay from "./stats-overlay";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

const stats: PlayerStats = {
  width: 1920,
  height: 1080,
  viewportWidth: 1280,
  viewportHeight: 720,
  buffered: 1,
  totalFrames: 30,
  level: -1,
  liveEdgeDelay: 1.5,
  playbackDelayMs: 1200,
  publicationBatchMs: 1000,
  avSyncMs: -42,
  roundTripTimeMs: 20,
};
let root: Root;
let container: HTMLDivElement;

beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  vi.stubGlobal(
    "ResizeObserver",
    class {
      constructor(private callback: ResizeObserverCallback) {}
      observe(target: Element) {
        this.callback(
          [
            {
              target,
              contentRect: { width: 480, height: 160 },
            } as ResizeObserverEntry,
          ],
          this as unknown as ResizeObserver,
        );
      }
      unobserve() {}
      disconnect() {}
    },
  );
  vi.stubGlobal("matchMedia", () => ({
    matches: false,
    addEventListener() {},
    removeEventListener() {},
  }));
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(
    function (this: HTMLElement) {
      const width =
        this.tagName === "SPAN" ? (this.textContent?.length ?? 0) * 8 : 480;
      return {
        x: 0,
        y: 0,
        width,
        height: 160,
        top: 0,
        left: 0,
        right: width,
        bottom: 160,
        toJSON() {},
      };
    },
  );
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
});

afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

async function render(value = stats) {
  await act(async () =>
    root.render(
      <StatsOverlay
        stats={value}
        live
        protocol="MoQ"
        latencyMode="Low"
        renderSurface="canvas"
        sessionId="test-session"
        onClose={() => {}}
      />,
    ),
  );
}

it("shows all reported readings and raw fields without a modal or backdrop", async () => {
  await render({ ...stats, bitrate: 6_000_000 });
  expect(container.textContent).toContain("1.50 s");
  const panel = container.querySelector("[data-player-diagnostics]")!;
  expect(document.querySelector('[data-slot="sheet-overlay"]')).toBeNull();
  expect(document.querySelector('[aria-modal="true"]')).toBeNull();
  expect(panel.textContent).toContain("player-stats-history-window");
  expect(
    panel.querySelector('[title="player-stats-broadcaster-delay-help"]'),
  ).not.toBeNull();
  expect(panel.querySelector("details")).toBeNull();
  expect(panel.textContent).not.toContain("player-stats-no-processing");
  expect(panel.textContent).toContain("test-session");
  await render({ ...stats, bitrate: 6_000_000, liveEdgeDelay: 2 });
  expect(panel.querySelectorAll(".recharts-surface")).toHaveLength(2);
  expect(panel.querySelector('[data-stat="avSyncMs"]')).not.toBeNull();
  expect(panel.querySelector("select")).toBeNull();
  expect(container.textContent).toContain("2.00 s");
});

it("keeps a metric's color when another reading becomes available", async () => {
  await render({ ...stats, availableBandwidth: 4_000_000 });
  const swatch = () =>
    document.querySelector<HTMLElement>(
      '[data-stat="availableBandwidth"] dt span',
    )!;
  const color = swatch().style.background;
  expect(color).not.toBe("");
  await render({ ...stats, availableBandwidth: 4_000_000, bitrate: 2_000_000 });
  expect(swatch().style.background).toBe(color);
});

it("expires readings without another report and recovers on a fresh sample", async () => {
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "performance"] });
  try {
    await render();
    await act(async () => vi.advanceTimersByTimeAsync(3000));
    expect(container.textContent).toContain("player-stats-stale");
    expect(container.textContent).not.toContain("1.50 s");
    const panel = document.querySelector("[data-player-diagnostics]")!;
    expect(panel.querySelectorAll(".recharts-surface")).toHaveLength(0);
    expect(panel.textContent).not.toContain("1.50 s");
    await render({ ...stats, liveEdgeDelay: 2 });
    expect(container.textContent).not.toContain("player-stats-stale");
    expect(container.textContent).toContain("2.00 s");
    expect(panel.querySelectorAll(".recharts-surface").length).toBeGreaterThan(
      0,
    );
    await act(async () => root.unmount());
    // Flush pending UI animation-frame fallbacks after unmount.
    await act(async () => vi.advanceTimersByTimeAsync(100));
    expect(vi.getTimerCount()).toBe(0);
  } finally {
    vi.useRealTimers();
  }
});

it("commits each incoming snapshot once, with its history already updated", async () => {
  vi.spyOn(performance, "now").mockReturnValue(1000);
  const commits: string[] = [];
  const update = (value: PlayerStats) =>
    act(async () =>
      root.render(
        <Profiler id="stats" onRender={(_, phase) => commits.push(phase)}>
          <StatsOverlay
            stats={value}
            live
            protocol="MoQ"
            latencyMode="Low"
            renderSurface="canvas"
            sessionId="test-session"
            onClose={() => {}}
          />
        </Profiler>,
      ),
    );
  await update(stats);
  commits.length = 0;
  await update({ ...stats, liveEdgeDelay: 2 });
  expect(commits).toEqual(["update"]);
  expect(container.textContent).toContain("2.00 s");
});
