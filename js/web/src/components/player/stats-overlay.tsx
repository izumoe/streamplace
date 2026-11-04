import { Button } from "@/components/ui/button";
import { XIcon } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  CartesianGrid,
  Line,
  LineChart,
  ReferenceLine,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";
import type { PlayerStats } from "./player";
import {
  appendStatsSample,
  getMetricValue,
  STATS_METRICS,
  STATS_WINDOW_MS,
  type StatsGroup,
  type StatsMetric,
  type StatsSample,
} from "./stats-history";

const GROUPS: StatsGroup[] = [
  "playback",
  "processing",
  "connection",
  "bandwidth",
];
const COLORS = [
  "var(--primary)",
  "var(--secondary)",
  "var(--foreground)",
  "var(--muted-foreground)",
];
const STATS_STALE_MS = 3000;

export default function StatsOverlay({
  stats,
  live,
  protocol,
  latencyMode,
  renderSurface,
  sessionId,
  onClose,
}: {
  stats: PlayerStats;
  live: boolean;
  protocol: string;
  latencyMode: string;
  renderSurface: "canvas" | "video";
  sessionId: string;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const [history, setHistory] = useState<StatsSample[]>(() =>
    appendStatsSample([], stats, performance.now()),
  );
  const sample = history.at(-1)!;
  // Synchronize before rendering children, so plots and current values agree.
  if (sample.stats !== stats)
    setHistory(appendStatsSample(history, stats, performance.now()));
  const [expiredSample, setExpiredSample] = useState<PlayerStats | null>(null);
  const stale = expiredSample === stats;
  useEffect(() => {
    const timeout = setTimeout(
      () => setExpiredSample(stats),
      Math.max(0, sample.at + STATS_STALE_MS - performance.now()),
    );
    return () => clearTimeout(timeout);
  }, [stats, sample.at]);

  return (
    <div
      role="dialog"
      aria-label={t("player-stats")}
      data-player-diagnostics
      onClick={(event) => event.stopPropagation()}
      onKeyDown={(event) => {
        if (event.key === "Escape" && !event.defaultPrevented) {
          event.stopPropagation();
          onClose();
        }
      }}
      className="dark bg-background/75 text-foreground border-border/50 absolute top-3 left-3 z-20 flex max-h-[calc(100%-var(--spacing)*6)] w-112 max-w-[calc(50%-var(--spacing)*3)] flex-col overflow-hidden rounded-sm border font-mono text-xs **:font-mono **:text-xs **:leading-tight"
    >
      <div className="flex shrink-0 items-center justify-between gap-2 py-1 pr-1 pl-2">
        <div className="flex min-w-0 flex-wrap items-baseline gap-x-2">
          <h3 className="flex items-center gap-2 font-medium">
            {t("player-stats")}
          </h3>
          <p className="text-muted-foreground">
            {protocol} · {latencyMode} · {renderSurface}
            {stats.width > 0 && stats.height > 0
              ? ` · ${stats.width}×${stats.height}`
              : ""}{" "}
            · {t("player-stats-history-short")}
          </p>
        </div>
        <Button
          variant="ghost"
          size="icon-touch"
          className="pointer-fine:size-6"
          aria-label={t("close")}
          onClick={onClose}
        >
          <XIcon className="size-3" aria-hidden />
        </Button>
      </div>
      <div data-stats-scroll className="min-h-0 overflow-y-auto px-2 pb-2">
        <p className="sr-only">{t("player-stats-history-window")}</p>
        {stale && (
          <p className="text-muted-foreground">{t("player-stats-stale")}</p>
        )}
        <div className="grid grid-cols-1 gap-0">
          {GROUPS.map((group) => (
            <StatsReadings
              key={group}
              group={group}
              history={history}
              stats={stale ? null : stats}
            />
          ))}
        </div>
        {!stale && (
          <dl className="border-border/50 mt-1 space-y-0 border-t pt-1">
            <Row
              label={t("player-stats-viewport")}
              value={`${stats.viewportWidth}×${stats.viewportHeight}`}
            />
            {stats.fps !== undefined && (
              <Row label="FPS" value={stats.fps.toFixed(1)} />
            )}
            {stats.droppedFrames !== undefined && (
              <Row
                label={t("player-stats-skipped")}
                value={`${stats.droppedFrames} / ${stats.totalFrames}`}
              />
            )}
            {stats.codecs && (
              <Row label={t("player-stats-codecs")} value={stats.codecs} />
            )}
            {stats.hlsVersion && (
              <Row label="hls.js version" value={stats.hlsVersion} />
            )}
            <Row label={t("player-stats-session")} value={sessionId} />
          </dl>
        )}
        {live && (
          <dl className="border-border/50 mt-1 border-t pt-1">
            <Row
              label={t("player-stats-broadcaster-delay")}
              value={t("player-stats-not-measured")}
              title={t("player-stats-broadcaster-delay-help")}
            />
            <Row
              label={t("player-stats-server-processing")}
              value={t("player-stats-not-measured")}
              title={t("player-stats-server-processing-help")}
            />
          </dl>
        )}
      </div>
    </div>
  );
}

function StatsReadings({
  group,
  history,
  stats,
}: {
  group: StatsGroup;
  history: StatsSample[];
  stats: PlayerStats | null;
}) {
  const { t } = useTranslation();
  const groupMetrics = STATS_METRICS.filter((metric) => metric.group === group);
  const metrics = groupMetrics.filter((metric) =>
    history.some((point) => getMetricValue(point.stats, metric) !== null),
  );
  const end = history.at(-1)?.at ?? 0;
  const populated = history.filter((point) => point.stats !== null).length;
  if (stats === null || metrics.length === 0) return null;
  const showChart = group === "playback" || group === "bandwidth";
  return (
    <section
      aria-label={t(`player-stats-group-${group}`)}
      className="grid min-w-0 grid-cols-[minmax(0,1fr)_minmax(0,2fr)] items-center gap-x-2"
    >
      {showChart && (
        <>
          <h4
            className="text-muted-foreground"
            title={t(`player-stats-group-${group}-help`)}
          >
            {t(`player-stats-group-${group}`)}
          </h4>
          {populated < 2 ? (
            <p className="text-muted-foreground">
              {t("player-stats-collecting")}
            </p>
          ) : (
            <div
              className="h-8 min-w-0"
              role="group"
              aria-label={t("player-stats-chart-label", {
                group: t(`player-stats-group-${group}`),
              })}
            >
              <ResponsiveContainer width="100%" height="100%">
                <LineChart data={history} accessibilityLayer>
                  <CartesianGrid vertical={false} stroke="var(--border)" />
                  <XAxis
                    hide
                    dataKey="at"
                    type="number"
                    domain={[end - STATS_WINDOW_MS, end]}
                  />
                  <YAxis
                    hide
                    domain={[
                      (min: number) => Math.min(0, min),
                      (max: number) => Math.max(1, max),
                    ]}
                  />
                  <ReferenceLine y={0} stroke="var(--border)" />
                  <Tooltip
                    isAnimationActive={false}
                    contentStyle={{
                      background: "var(--popover)",
                      color: "var(--popover-foreground)",
                      borderColor: "var(--border)",
                    }}
                    labelFormatter={(at) =>
                      `${Math.round((Number(at) - end) / 1000)}s`
                    }
                    formatter={(value, name) => [
                      formatValue(
                        Number(value),
                        group === "bandwidth" ? "Mbps" : "ms",
                      ),
                      name,
                    ]}
                  />
                  {metrics.map((metric) => (
                    <Line
                      key={metric.key}
                      name={t(metric.label)}
                      dataKey={(point: StatsSample) =>
                        getMetricValue(point.stats, metric)
                      }
                      type="linear"
                      stroke={COLORS[groupMetrics.indexOf(metric)]}
                      strokeDasharray={
                        groupMetrics.indexOf(metric) === 1
                          ? "var(--spacing) var(--spacing)"
                          : undefined
                      }
                      strokeWidth="calc(var(--spacing) / 2)"
                      dot={false}
                      connectNulls={false}
                      isAnimationActive={false}
                    />
                  ))}
                </LineChart>
              </ResponsiveContainer>
            </div>
          )}
        </>
      )}
      <dl className="col-span-2 space-y-0 text-xs">
        {metrics.map((metric) => (
          <div
            key={metric.key}
            data-stat={metric.key}
            className="flex items-start justify-between gap-1"
          >
            <dt
              title={t(`${metric.label}-help`)}
              className="text-muted-foreground flex min-w-0 items-start gap-1"
            >
              {showChart && (
                <span
                  className="mt-1.5 h-0.5 w-1 shrink-0"
                  style={{ background: COLORS[groupMetrics.indexOf(metric)] }}
                  aria-hidden
                />
              )}
              <span className="min-w-0 break-words">{t(metric.label)}</span>
            </dt>
            <dd className="shrink-0 font-mono tabular-nums">
              {formatValue(getMetricValue(stats, metric), metric.unit)}
            </dd>
          </div>
        ))}
      </dl>
    </section>
  );
}

function formatValue(value: number | null, unit: StatsMetric["unit"]) {
  if (value === null || !Number.isFinite(value)) return "—";
  if (unit === "ms" && Math.abs(value) >= 1000)
    return `${(value / 1000).toFixed(2)} s`;
  return `${value.toFixed(unit === "Mbps" ? 2 : 1)} ${unit}`;
}

function Row({
  label,
  value,
  title,
}: {
  label: string;
  value: string;
  title?: string;
}) {
  return (
    <div title={title} className="flex items-start justify-between gap-1">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="min-w-0 text-right break-all tabular-nums">{value}</dd>
    </div>
  );
}
