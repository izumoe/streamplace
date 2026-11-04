import { expect, it } from "vitest";
import { getWebRTCVideoTiming } from "./webrtc-stats";

it("reports receiver processing, decode and jitter buffer averages in milliseconds", () => {
  expect(
    getWebRTCVideoTiming({
      framesDecoded: 20,
      totalProcessingDelay: 0.4,
      totalDecodeTime: 0.1,
      jitterBufferDelay: 0.3,
      jitterBufferEmittedCount: 10,
    }),
  ).toEqual({ processingDelayMs: 20, decodeTimeMs: 5, jitterBufferMs: 30 });
});

it("uses each counter's denominator and retains real zero delays", () => {
  expect(
    getWebRTCVideoTiming({
      framesDecoded: 0,
      totalProcessingDelay: 1,
      totalDecodeTime: 1,
      jitterBufferDelay: 0,
      jitterBufferEmittedCount: 10,
    }),
  ).toEqual({
    processingDelayMs: undefined,
    decodeTimeMs: undefined,
    jitterBufferMs: 0,
  });
});

it.each([undefined, -1, NaN, Infinity])(
  "omits unavailable or invalid timing counters (%s)",
  (value) => {
    expect(
      getWebRTCVideoTiming({
        framesDecoded: 1,
        totalProcessingDelay: value,
        totalDecodeTime: value,
        jitterBufferDelay: value,
        jitterBufferEmittedCount: 1,
      }),
    ).toEqual({
      processingDelayMs: undefined,
      decodeTimeMs: undefined,
      jitterBufferMs: undefined,
    });
    expect(
      getWebRTCVideoTiming({
        framesDecoded: value,
        totalProcessingDelay: 0,
        totalDecodeTime: 0,
        jitterBufferDelay: 0,
        jitterBufferEmittedCount: value,
      }),
    ).toEqual({
      processingDelayMs: undefined,
      decodeTimeMs: undefined,
      jitterBufferMs: undefined,
    });
  },
);
