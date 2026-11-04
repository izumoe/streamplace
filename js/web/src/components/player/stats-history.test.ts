import { expect, it } from "vitest";
import type { PlayerStats } from "./player";
import {
  appendStatsSample,
  getMetricValue,
  STATS_METRICS,
  type StatsSample,
} from "./stats-history";

const stats: PlayerStats = {
  width: 1920,
  height: 1080,
  viewportWidth: 1280,
  viewportHeight: 720,
  buffered: 1,
  totalFrames: 30,
  level: -1,
};

it("keeps a bounded minute of real samples, with no invented prehistory", () => {
  let history: StatsSample[] = [];
  history = appendStatsSample(history, stats, 0);
  expect(history).toHaveLength(1);
  for (let at = 1000; at <= 120_000; at += 1000)
    history = appendStatsSample(history, { ...stats }, at);
  expect(history).toHaveLength(61);
  expect(history[0].at).toBe(60_000);
  expect(history.at(-1)?.at).toBe(120_000);
  // Cap memory even if a backend starts reporting more often than expected.
  for (let at = 120_001; at <= 120_100; at++)
    history = appendStatsSample(history, { ...stats }, at);
  expect(history).toHaveLength(61);
});

it("breaks the line across a missed poll and preserves the actual elapsed time", () => {
  const first = appendStatsSample([], stats, 1000);
  const next = appendStatsSample(first, { ...stats }, 3000);
  expect(first).toHaveLength(1);
  expect(
    next.map((point) => [point.at, point.stats?.buffered ?? null]),
  ).toEqual([
    [1000, 1],
    [2000, null],
    [3000, 1],
  ]);
});

it("does not duplicate a sample during strict-mode effect replay", () => {
  const first = appendStatsSample([], stats, 1000);
  expect(appendStatsSample(first, stats, 1000)).toBe(first);
});

it("converts seconds and bits per second to each chart's units", () => {
  const values = {
    ...stats,
    liveEdgeDelay: 1.5,
    bitrate: 6_000_000,
    avSyncMs: -42,
  };
  const metric = (key: string) =>
    STATS_METRICS.find((metric) => metric.key === key)!;
  expect(getMetricValue(values, metric("liveEdgeDelay"))).toBe(1500);
  expect(getMetricValue(values, metric("buffered"))).toBe(1000);
  expect(getMetricValue(values, metric("bitrate"))).toBe(6);
  expect(getMetricValue(values, metric("avSyncMs"))).toBe(-42);
});

it.each([undefined, NaN, Infinity, -1])(
  "keeps unavailable and invalid measurements as graph gaps (%s)",
  (value) => {
    const metric = STATS_METRICS.find(
      (metric) => metric.key === "decodeTimeMs",
    )!;
    expect(
      getMetricValue({ ...stats, decodeTimeMs: value }, metric),
    ).toBeNull();
    expect(getMetricValue(null, metric)).toBeNull();
    expect(getMetricValue({ ...stats, decodeTimeMs: 0 }, metric)).toBe(0);
  },
);
