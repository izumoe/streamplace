import { expect, test } from "@playwright/test";
import {
  analyzeCadence,
  analyzePresentation,
  analyzeRtp,
} from "./live-latency-analysis";

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

test("latency RTP health: bounds and real observation interval determine bitrate and codec", () => {
  const snapshots = [
    { at: -100, reports: [{ id: "v", type: "outbound-rtp", bytesSent: 0 }] },
    {
      at: 100,
      reports: [
        {
          id: "v",
          type: "outbound-rtp",
          kind: "video",
          codecId: "h264",
          bytesSent: 1000,
          framesEncoded: 3,
        },
      ],
    },
    {
      at: 1100,
      reports: [
        {
          id: "v",
          type: "outbound-rtp",
          kind: "video",
          codecId: "h264",
          bytesSent: 51000,
          framesEncoded: 33,
          frameWidth: 640,
          frameHeight: 360,
          framesPerSecond: 30,
          qualityLimitationReason: "none",
        },
        {
          id: "h264",
          type: "codec",
          mimeType: "video/H264",
          sdpFmtpLine: "packetization-mode=1;profile-level-id=42e01f",
          clockRate: 90000,
        },
      ],
    },
    {
      at: 2000,
      reports: [{ id: "v", type: "outbound-rtp", bytesSent: 900000 }],
    },
  ];
  const [result] = analyzeRtp(snapshots, 0, 2000);
  expect(result).toMatchObject({
    kind: "video",
    observationMs: 1000,
    bitrateBps: 400000,
    measuredFps: 30,
    frameWidth: 640,
    frameHeight: 360,
    reportedFps: 30,
    codec: {
      mimeType: "video/H264",
      sdpFmtpLine: "packetization-mode=1;profile-level-id=42e01f",
    },
    qualityLimitationReason: "none",
  });
});

test("latency RTP health: missing observations and counter resets do not become zero or negative throughput", () => {
  expect(analyzeRtp([], 0, 2000)).toEqual([]);
  const report = {
    id: "a",
    type: "inbound-rtp",
    kind: "audio",
    bytesReceived: 1000,
  };
  const [single] = analyzeRtp([{ at: 100, reports: [report] }], 0, 2000);
  expect(single.bitrateBps).toBeNull();
  expect(single.measuredFps).toBeNull();
  expect(single.codec).toBeNull();
  const [reset] = analyzeRtp(
    [
      { at: 100, reports: [report] },
      {
        at: 1100,
        reports: [
          {
            ...report,
            bytesReceived: 100,
            concealedSamples: 240,
            insertedSamplesForDeceleration: 0,
          },
        ],
      },
    ],
    0,
    2000,
  );
  expect(reset.bitrateBps).toBeNull();
  expect(reset.counterReset).toBe(true);
  expect(reset.health.concealedSamples).toBeNull();
});
