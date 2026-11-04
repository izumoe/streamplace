import { Button } from "@/components/ui/button";
import {
  Sheet,
  SheetClose,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
  SheetTrigger,
} from "@/components/ui/sheet";
import { ActivityIcon, ArrowUpRightIcon, XIcon } from "lucide-react";
import { useEffect, useState, type RefObject } from "react";
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
  "var(--chart-1)",
  "var(--chart-2)",
  "var(--chart-3)",
  "var(--chart-4)",
];
const STATS_STALE_MS = 3000;

export default function StatsOverlay({
  stats,
  live,
  protocol,
  latencyMode,
  renderSurface,
  sessionId,
  portalContainer,
}: {
  stats: PlayerStats;
  live: boolean;
  protocol: string;
  latencyMode: string;
  renderSurface: "canvas" | "video";
  sessionId: string;
  portalContainer?: RefObject<HTMLDivElement | null>;
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

  const compact = [
    "liveEdgeDelay",
    "processingDelayMs",
    "buffered",
    "avSyncMs",
    "decodeTimeMs",
  ]
    .map((key) => STATS_METRICS.find((metric) => metric.key === key)!)
    .filter((metric) => getMetricValue(stats, metric) !== null)
    .slice(0, 3);
  const primary = compact[0];
  return (
    <div
      role="dialog"
      aria-label={t("player-stats")}
      onClick={(event) => event.stopPropagation()}
      className="bg-background/95 text-foreground absolute top-4 right-4 z-20 max-h-[calc(100%-var(--spacing)*8)] w-64 max-w-[calc(100%-var(--spacing)*8)] overflow-y-auto rounded-lg border p-3 font-mono text-xs shadow-sm"
    >
      <div className="text-muted-foreground flex items-center justify-between gap-2">
        <span className="flex items-center gap-2">
          <ActivityIcon className="size-3" aria-hidden />
          {t("player-stats")}
        </span>
        <span>{protocol}</span>
      </div>
      {stale && (
        <p role="status" className="text-muted-foreground my-2">
          {t("player-stats-stale")}
        </p>
      )}
      {!stale && primary && (
        <div className="my-2">
          <div className="text-muted-foreground">{t(primary.label)}</div>
          <div className="mt-1 text-2xl font-medium tracking-tight tabular-nums">
            {formatValue(getMetricValue(stats, primary), primary.unit)}
          </div>
        </div>
      )}
      <dl className="space-y-1">
        {!stale &&
          compact
            .slice(1)
            .map((metric) => (
              <Row
                key={metric.key}
                label={t(metric.label)}
                value={formatValue(getMetricValue(stats, metric), metric.unit)}
              />
            ))}
      </dl>
      <Sheet>
        <SheetTrigger
          render={
            <Button
              variant="ghost"
              className="mt-2 min-h-11 w-full justify-between"
              aria-label={t("player-stats-open-diagnostics")}
            />
          }
        >
          {t("player-stats-open-diagnostics")}
          <ArrowUpRightIcon aria-hidden />
        </SheetTrigger>
        <SheetContent
          portalContainer={portalContainer}
          showCloseButton={false}
          backdropClassName="bg-transparent! supports-backdrop-filter:backdrop-blur-none!"
          data-player-diagnostics
          className="gap-0! overflow-y-auto transition-none! data-[side=right]:w-full! data-[side=right]:sm:max-w-3xl!"
        >
          <SheetHeader className="bg-popover sticky top-0 z-10 border-b p-4 pr-16">
            <SheetClose
              render={
                <Button
                  variant="ghost"
                  size="icon-touch"
                  className="absolute top-2 right-2"
                  aria-label={t("close")}
                />
              }
            >
              <XIcon aria-hidden />
            </SheetClose>
            <SheetTitle>{t("player-stats-diagnostics")}</SheetTitle>
            <SheetDescription>
              {protocol}
              {stats.width > 0 && stats.height > 0
                ? ` · ${stats.width}×${stats.height}`
                : ""}
            </SheetDescription>
            <p className="text-muted-foreground mt-2 text-xs">
              {t("player-stats-history-window")}
            </p>
          </SheetHeader>
          <div className="grid gap-4 p-4 sm:grid-cols-2">
            {GROUPS.map((group) => (
              <StatsChart
                key={group}
                group={group}
                history={history}
                stats={stale ? null : stats}
              />
            ))}
          </div>
          {live && (
            <div className="text-muted-foreground mx-4 mb-4 space-y-2 rounded-lg border border-dashed p-3 text-xs">
              <p>
                <span className="text-foreground font-medium">
                  {t("player-stats-broadcaster-delay")} ·{" "}
                  {t("player-stats-not-measured")}
                </span>
                <br />
                {t("player-stats-broadcaster-delay-help")}
              </p>
              <p>
                <span className="text-foreground font-medium">
                  {t("player-stats-server-processing")} ·{" "}
                  {t("player-stats-not-measured")}
                </span>
                <br />
                {t("player-stats-server-processing-help")}
              </p>
            </div>
          )}
          {!stale && (
            <details className="mx-4 mb-4 rounded-lg border p-3 text-xs">
              <summary className="focus-visible:ring-ring cursor-pointer rounded font-medium focus-visible:ring-2">
                {t("player-stats-stream-details")}
              </summary>
              <dl className="mt-3 space-y-2 font-mono">
                <Row
                  label={t("player-stats-resolution")}
                  value={`${stats.width}×${stats.height}`}
                />
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
                <Row label={t("player-stats-protocol")} value={protocol} />
                <Row
                  label={t("player-stats-latency-mode")}
                  value={latencyMode}
                />
                <Row
                  label={t("player-stats-render-surface")}
                  value={renderSurface}
                />
                {stats.hlsVersion && (
                  <Row label="hls.js version" value={stats.hlsVersion} />
                )}
                <Row label={t("player-stats-session")} value={sessionId} />
              </dl>
            </details>
          )}
        </SheetContent>
      </Sheet>
    </div>
  );
}

function StatsChart({
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
  return (
    <section
      aria-label={t(`player-stats-group-${group}`)}
      className="min-w-0 rounded-lg border p-3"
    >
      <h3 className="text-sm font-medium">
        {t(`player-stats-group-${group}`)}
      </h3>
      <p className="text-muted-foreground mt-1 min-h-12 text-xs">
        {t(`player-stats-group-${group}-help`)}
      </p>
      {stats === null ? (
        <p className="text-muted-foreground flex h-40 items-center text-xs">
          {t("player-stats-stale")}
        </p>
      ) : metrics.length === 0 ? (
        <p className="text-muted-foreground flex h-40 items-center text-xs">
          {t(
            group === "processing"
              ? "player-stats-no-processing"
              : "player-stats-no-data",
          )}
        </p>
      ) : populated < 2 ? (
        <p className="text-muted-foreground flex h-40 items-center text-xs">
          {t("player-stats-collecting")}
        </p>
      ) : (
        <div
          className="mt-3 h-40 min-w-0"
          role="group"
          aria-label={t("player-stats-chart-label", {
            group: t(`player-stats-group-${group}`),
          })}
        >
          <ResponsiveContainer width="100%" height="100%">
            <LineChart data={history} accessibilityLayer>
              <CartesianGrid vertical={false} stroke="var(--border)" />
              <XAxis
                dataKey="at"
                type="number"
                domain={[end - STATS_WINDOW_MS, end]}
                ticks={[end - 60_000, end - 30_000, end]}
                tickFormatter={(at) => `${Math.round((at - end) / 1000)}s`}
                tickLine={false}
                axisLine={false}
                stroke="var(--muted-foreground)"
              />
              <YAxis
                width="auto"
                domain={[
                  (min: number) => Math.min(0, min),
                  (max: number) => Math.max(1, max),
                ]}
                tickFormatter={(value) =>
                  formatValue(value, group === "bandwidth" ? "Mbps" : "ms")
                }
                tickLine={false}
                axisLine={false}
                stroke="var(--muted-foreground)"
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
      <dl className="mt-3 space-y-2 text-xs">
        {metrics.map((metric) => (
          <div
            key={metric.key}
            data-stat={metric.key}
            className="flex items-start justify-between gap-3"
          >
            <dt
              title={t(`${metric.label}-help`)}
              className="text-muted-foreground flex items-start gap-2"
            >
              <span
                className="mt-1.5 h-0.5 w-3 shrink-0"
                style={{ background: COLORS[groupMetrics.indexOf(metric)] }}
                aria-hidden
              />
              {t(metric.label)}
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

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-start justify-between gap-3">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="min-w-0 text-right break-all tabular-nums">{value}</dd>
    </div>
  );
}
