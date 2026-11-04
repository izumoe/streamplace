import type { PlayerStats } from "./player";

export type StatsSample = { at: number; stats: PlayerStats | null };
export type StatsGroup = "playback" | "processing" | "connection" | "bandwidth";
export type StatsMetric = {
  key: keyof PlayerStats;
  label: string;
  group: StatsGroup;
  unit: "ms" | "Mbps";
  scale?: number;
  signed?: boolean;
};

export const STATS_WINDOW_MS = 60_000;
const MAX_SAMPLES = 61;

export const STATS_METRICS: StatsMetric[] = [
  {
    key: "liveEdgeDelay",
    label: "player-stats-live-edge-delay",
    group: "playback",
    unit: "ms",
    scale: 1000,
  },
  {
    key: "playbackDelayMs",
    label: "player-stats-playback-delay",
    group: "playback",
    unit: "ms",
  },
  {
    key: "publicationBatchMs",
    label: "player-stats-publication-batch",
    group: "playback",
    unit: "ms",
  },
  {
    key: "buffered",
    label: "player-stats-buffer",
    group: "playback",
    unit: "ms",
    scale: 1000,
  },
  {
    key: "processingDelayMs",
    label: "player-stats-processing-delay",
    group: "processing",
    unit: "ms",
  },
  {
    key: "decodeTimeMs",
    label: "player-stats-decode-time",
    group: "processing",
    unit: "ms",
  },
  {
    key: "jitterBufferMs",
    label: "player-stats-jitter-buffer",
    group: "processing",
    unit: "ms",
  },
  {
    key: "roundTripTimeMs",
    label: "player-stats-rtt",
    group: "connection",
    unit: "ms",
  },
  {
    key: "avSyncMs",
    label: "player-stats-av-sync",
    group: "connection",
    unit: "ms",
    signed: true,
  },
  {
    key: "ttfbEstimate",
    label: "player-stats-ttfb",
    group: "connection",
    unit: "ms",
  },
  {
    key: "bitrate",
    label: "player-stats-bitrate",
    group: "bandwidth",
    unit: "Mbps",
    scale: 0.000001,
  },
  {
    key: "renditionBitrate",
    label: "player-stats-rendition-bitrate",
    group: "bandwidth",
    unit: "Mbps",
    scale: 0.000001,
  },
  {
    key: "availableBandwidth",
    label: "player-stats-available-bandwidth",
    group: "bandwidth",
    unit: "Mbps",
    scale: 0.000001,
  },
];

export function getMetricValue(stats: PlayerStats | null, metric: StatsMetric) {
  const value = stats?.[metric.key];
  if (
    typeof value !== "number" ||
    !Number.isFinite(value) ||
    (!metric.signed && value < 0)
  )
    return null;
  const scaled = value * (metric.scale ?? 1);
  return Number.isFinite(scaled) ? scaled : null;
}

export function appendStatsSample(
  history: StatsSample[],
  stats: PlayerStats,
  at: number,
): StatsSample[] {
  const last = history.at(-1);
  if (last?.stats === stats) return history;
  // Reduced-precision clocks can give distinct snapshots the same timestamp.
  if (last && at <= last.at)
    return [...history.slice(0, -1), { at: last.at, stats }];
  const next = history.filter((point) => point.at >= at - STATS_WINDOW_MS);
  // Do not draw an apparently healthy line through a missing polling interval.
  if (next.length && last && at - last.at > 1500)
    next.push({ at: last.at + 1000, stats: null });
  next.push({ at, stats });
  return next.slice(-MAX_SAMPLES);
}
