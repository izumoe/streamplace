export type PresentationSample = {
  id: number | null;
  display: number;
  latencyMs: number | null;
};

export function analyzeCadence(
  timestamps: number[],
  start: number,
  end: number,
) {
  const valid = timestamps
    .filter((at) => Number.isFinite(at) && at >= start && at < end)
    .sort((a, b) => a - b);
  let previous = start;
  let maxGapMs = 0;
  for (const at of valid) {
    maxGapMs = Math.max(maxGapMs, at - previous);
    previous = at;
  }
  return {
    samples: valid.length,
    fps: (valid.length * 1000) / (end - start),
    maxGapMs: Math.max(maxGapMs, end - previous),
  };
}

export function analyzePresentation(
  frames: PresentationSample[],
  start: number,
  end: number,
) {
  const valid = frames.filter(
    (frame) =>
      frame.id !== null &&
      Number.isFinite(frame.display) &&
      frame.display >= start &&
      frame.display < end &&
      frame.latencyMs !== null &&
      Number.isFinite(frame.latencyMs) &&
      frame.latencyMs >= 0,
  );
  function median(values: number[]) {
    if (!values.length) return null;
    values.sort((a, b) => a - b);
    const middle = Math.floor(values.length / 2);
    return values.length % 2
      ? values[middle]
      : (values[middle - 1] + values[middle]) / 2;
  }
  const latencies = valid.map((frame) => frame.latencyMs!);
  const p50Ms = median([...latencies]);
  const midpoint = (start + end) / 2;
  const first = median(
    valid
      .filter((frame) => frame.display < midpoint)
      .map((frame) => frame.latencyMs!),
  );
  const last = median(
    valid
      .filter((frame) => frame.display >= midpoint)
      .map((frame) => frame.latencyMs!),
  );
  const cadence = analyzeCadence(
    valid.map((frame) => frame.display),
    start,
    end,
  );
  const meanDisplay =
    valid.reduce((sum, frame) => sum + frame.display, 0) / valid.length;
  const meanLatency =
    latencies.reduce((sum, latency) => sum + latency, 0) / valid.length;
  let variance = 0;
  let covariance = 0;
  for (const frame of valid) {
    variance += (frame.display - meanDisplay) ** 2;
    covariance +=
      (frame.display - meanDisplay) * (frame.latencyMs! - meanLatency);
  }
  latencies.sort((a, b) => a - b);
  return {
    ...cadence,
    p50Ms,
    p95Ms: latencies.length
      ? latencies[Math.ceil(latencies.length * 0.95) - 1]
      : null,
    maxMs: latencies.length ? latencies[latencies.length - 1] : null,
    lagGrowthMs: first !== null && last !== null ? last - first : null,
    lagSlopeMsPerSecond: variance > 0 ? (covariance / variance) * 1000 : null,
  };
}
