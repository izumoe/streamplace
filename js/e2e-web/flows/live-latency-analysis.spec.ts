import { expect, test } from "@playwright/test";
import { analyzeCadence, analyzePresentation } from "./live-latency-analysis";

test("latency analysis: empty or corrupt samples cannot produce a latency", () => {
  const result = analyzePresentation(
    [
      { id: null, display: 100, latencyMs: 100 },
      { id: 2, display: 200, latencyMs: -1 },
      { id: 3, display: 300, latencyMs: NaN },
    ],
    0,
    1000,
  );
  expect(result.samples).toBe(0);
  expect(result.p50Ms).toBeNull();
  expect(result.p95Ms).toBeNull();
  expect(result.lagSlopeMsPerSecond).toBeNull();
  expect(result.maxGapMs).toBe(1000);
});

test("latency analysis: steady bounds exclude startup and preserve growing lag", () => {
  const result = analyzePresentation(
    [
      { id: 0, display: -100, latencyMs: 9000 },
      { id: 1, display: 0, latencyMs: 100 },
      { id: 2, display: 1000, latencyMs: 200 },
      { id: 3, display: 2000, latencyMs: 300 },
      { id: 4, display: 3000, latencyMs: 400 },
      { id: 5, display: 4000, latencyMs: 9000 },
    ],
    0,
    4000,
  );
  expect(result.samples).toBe(4);
  expect(result.p50Ms).toBe(250);
  expect(result.p95Ms).toBe(400);
  expect(result.maxMs).toBe(400);
  expect(result.lagGrowthMs).toBe(200);
  expect(result.lagSlopeMsPerSecond).toBeCloseTo(100);
  expect(result.maxGapMs).toBe(1000);
});

test("latency analysis: a late freeze remains visible through the window end", () => {
  const result = analyzePresentation(
    [
      { id: 1, display: 100, latencyMs: 200 },
      { id: 2, display: 200, latencyMs: 200 },
    ],
    0,
    5000,
  );
  expect(result.maxGapMs).toBe(4800);
  expect(result.fps).toBe(0.4);
  expect(result.lagSlopeMsPerSecond).toBe(0);
});

test("latency cadence: source health excludes invalid timestamps and retains a stall", () => {
  const timestamps = Array.from(
    { length: 600 },
    (_, index) => (index * 1000) / 30,
  );
  const healthy = analyzeCadence(
    [...timestamps, NaN, Infinity, -1, 20_000],
    0,
    20_000,
  );
  expect(healthy.samples).toBe(600);
  expect(healthy.fps).toBe(30);
  expect(healthy.maxGapMs).toBeCloseTo(1000 / 30);
  const stalled = analyzeCadence(
    timestamps.filter((at) => at < 10_000),
    0,
    20_000,
  );
  expect(stalled.fps).toBe(15);
  expect(stalled.maxGapMs).toBeGreaterThan(10_000);
});
