// All counters are cumulative since this inbound RTP stream started.
export function getWebRTCVideoTiming(stats: Partial<RTCInboundRtpStreamStats>) {
  return {
    processingDelayMs: average(stats.totalProcessingDelay, stats.framesDecoded),
    decodeTimeMs: average(stats.totalDecodeTime, stats.framesDecoded),
    jitterBufferMs: average(
      stats.jitterBufferDelay,
      stats.jitterBufferEmittedCount,
    ),
  };
}

function average(seconds: number | undefined, count: number | undefined) {
  if (
    seconds === undefined ||
    !Number.isFinite(seconds) ||
    seconds < 0 ||
    count === undefined ||
    !Number.isFinite(count) ||
    count <= 0
  )
    return;
  return (seconds / count) * 1000;
}
