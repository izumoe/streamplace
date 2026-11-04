import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { ChevronDown, Clipboard } from "lucide-react";
import { useId, useMemo, useState } from "react";
import { Trans, useTranslation } from "react-i18next";
import { toast } from "sonner";
import {
  createERTMPConfig,
  ERTMP_AUDIO_BITRATE,
  ERTMP_ENCODERS,
  ERTMP_QUALITIES,
  type ERTMPEncoder,
} from "./ertmp-config";

const selectClass = "min-h-12 w-full min-w-0";
const summaryClass =
  "focus-visible:outline-ring flex min-h-12 cursor-pointer items-center justify-between gap-4 text-sm font-medium focus-visible:outline-2 focus-visible:outline-offset-2";

export function ERTMPSetup() {
  const { t } = useTranslation("common");
  const id = useId();
  const [encoder, setEncoder] = useState<ERTMPEncoder>("x264");
  const [resolution, setResolution] = useState<1080 | 1440>(1080);
  const [frameRate, setFrameRate] = useState<30 | 60>(30);
  const config = useMemo(
    () => createERTMPConfig({ encoder, resolution, frameRate }),
    [encoder, resolution, frameRate],
  );
  const json = config ? JSON.stringify(config, null, 2) : "";
  const totalBitrate = config?.encoder_configurations.reduce(
    (sum, quality) => sum + quality.settings.bitrate,
    ERTMP_AUDIO_BITRATE,
  );
  const estimatedUpload =
    totalBitrate === undefined ? undefined : (totalBitrate * 1.1) / 1000;
  const topQuality = ERTMP_QUALITIES.find(
    (quality) => quality.resolution === resolution,
  )!;

  async function copy() {
    if (!config) return;
    try {
      await navigator.clipboard.writeText(json);
      toast.success(t("copied-to-clipboard"));
    } catch {
      toast.error(t("share-copy-failed"));
    }
  }

  return (
    <div className="min-w-0 space-y-6">
      <div className="space-y-3">
        <div className="flex flex-wrap items-center gap-3">
          <h2 className="font-display text-lg font-semibold">
            {t("ertmp-title")}
          </h2>
          <span className="rounded-md border border-(--color-border) px-2 py-1 text-xs text-(--color-fg-muted)">
            {t("ertmp-experimental-label")}
          </span>
        </div>
        <p className="text-sm text-(--color-fg-muted)">
          {t("ertmp-subtitle")}. {t("ertmp-requires-obs")}
        </p>
        <p className="text-sm leading-relaxed">{t("ertmp-description")}</p>
        <p className="text-sm text-(--color-fg-muted)">
          {t("ertmp-experimental")}
        </p>
      </div>
      <div className="min-w-0 overflow-hidden rounded-lg border border-(--color-border) bg-(--color-bg-elevated)">
        <div className="space-y-6 p-4 sm:p-6">
          <h3 className="text-base font-semibold">{t("ertmp-encoder")}</h3>
          <div className="grid min-w-0 grid-cols-1 items-start gap-4 sm:grid-cols-2">
            <div className="min-w-0 space-y-2">
              <label htmlFor={`${id}-encoder`} className="block font-medium">
                {t("ertmp-encoder-field")}
              </label>
              <Select<ERTMPEncoder>
                value={encoder}
                onValueChange={(value) => {
                  if (value !== null) setEncoder(value);
                }}
              >
                <SelectTrigger id={`${id}-encoder`} className={selectClass}>
                  <SelectValue>{t(`ertmp-encoder-${encoder}`)}</SelectValue>
                </SelectTrigger>
                <SelectContent alignItemWithTrigger={false} align="start">
                  {(Object.keys(ERTMP_ENCODERS) as ERTMPEncoder[]).map(
                    (value) => (
                      <SelectItem
                        key={value}
                        value={value}
                        disabled={
                          resolution === 1440 && !ERTMP_ENCODERS[value].av1
                        }
                        className="min-h-12"
                      >
                        {t(`ertmp-encoder-${value}`)}
                      </SelectItem>
                    ),
                  )}
                </SelectContent>
              </Select>
            </div>
            <div className="min-w-0 space-y-2">
              <label htmlFor={`${id}-resolution`} className="block font-medium">
                {t("ertmp-top-quality")}
              </label>
              <Select<1080 | 1440>
                value={resolution}
                onValueChange={(value) => {
                  if (value !== null) setResolution(value);
                }}
              >
                <SelectTrigger
                  id={`${id}-resolution`}
                  aria-describedby={
                    resolution === 1440 ? `${id}-av1-help` : undefined
                  }
                  className={selectClass}
                >
                  <SelectValue>
                    {resolution}p · {resolution === 1440 ? "AV1" : "H.264"}
                  </SelectValue>
                </SelectTrigger>
                <SelectContent alignItemWithTrigger={false} align="start">
                  <SelectItem value={1080} className="min-h-12">
                    1080p · H.264
                  </SelectItem>
                  <SelectItem
                    value={1440}
                    disabled={!ERTMP_ENCODERS[encoder].av1}
                    className="min-h-12 [&>span]:whitespace-normal"
                  >
                    {ERTMP_ENCODERS[encoder].av1
                      ? "1440p · AV1"
                      : t("ertmp-1440-unavailable")}
                  </SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="min-w-0 space-y-2">
              <label htmlFor={`${id}-fps`} className="block font-medium">
                {t("ertmp-frame-rate")}
              </label>
              <Select<30 | 60>
                value={frameRate}
                onValueChange={(value) => {
                  if (value !== null) setFrameRate(value);
                }}
              >
                <SelectTrigger id={`${id}-fps`} className={selectClass}>
                  <SelectValue>{frameRate} fps</SelectValue>
                </SelectTrigger>
                <SelectContent alignItemWithTrigger={false} align="start">
                  <SelectItem value={30} className="min-h-12">
                    30 fps
                  </SelectItem>
                  <SelectItem value={60} className="min-h-12">
                    60 fps
                  </SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>
          {resolution === 1440 && (
            <p
              id={`${id}-av1-help`}
              className="text-xs leading-relaxed text-(--color-fg-muted)"
            >
              {t("ertmp-av1-requirements")}
            </p>
          )}
          <div className="space-y-2">
            <h4 id={`${id}-budget`} className="font-medium">
              {t("ertmp-video-qualities")}
            </h4>
            <table aria-labelledby={`${id}-budget`} className="w-full text-sm">
              <thead className="text-xs text-(--color-fg-muted)">
                <tr className="border-b border-(--color-border)">
                  <th scope="col" className="pb-2 text-left font-normal">
                    {t("ertmp-quality")}
                  </th>
                  <th scope="col" className="pb-2 text-right font-normal">
                    {t("ertmp-bitrate-kbps")}
                  </th>
                </tr>
              </thead>
              <tbody className="divide-y divide-(--color-border)">
                {ERTMP_QUALITIES.filter(
                  (quality) => quality.resolution <= resolution,
                ).map((quality) => (
                  <tr key={quality.resolution}>
                    <th
                      scope="row"
                      className="py-3 text-left font-medium tabular-nums"
                    >
                      {quality.resolution}p{" "}
                      <span className="ml-2 text-xs font-normal text-(--color-fg-muted)">
                        {quality.resolution === 1440 ? "AV1" : "H.264"}
                      </span>
                    </th>
                    <td className="py-3 text-right tabular-nums">
                      {quality.bitrate[frameRate] ?? quality.bitrate[30]}
                    </td>
                  </tr>
                ))}
                <tr>
                  <th scope="row" className="py-3 text-left font-medium">
                    {t("ertmp-audio-quality")}{" "}
                    <span className="ml-2 text-xs font-normal text-(--color-fg-muted)">
                      AAC
                    </span>
                  </th>
                  <td className="py-3 text-right tabular-nums">
                    {ERTMP_AUDIO_BITRATE}
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
        <div className="space-y-4 border-t border-(--color-border) bg-(--color-bg) p-4 sm:px-6">
          <div className="flex flex-wrap items-center justify-between gap-4">
            <div className="space-y-1">
              <p className="text-xs text-(--color-fg-muted)">
                {t("ertmp-target-upload")}
              </p>
              <output
                className="text-lg font-semibold tabular-nums"
                aria-live="polite"
                aria-describedby={`${id}-overhead`}
              >
                {estimatedUpload === undefined
                  ? "—"
                  : estimatedUpload.toFixed(1)}{" "}
                -{" "}
                {estimatedUpload === undefined
                  ? "—"
                  : Math.ceil(estimatedUpload * 1.25)}{" "}
                Mbps
              </output>
              <p
                id={`${id}-overhead`}
                className="text-xs text-(--color-fg-muted)"
              >
                {t("ertmp-budget-overhead")}
              </p>
            </div>
          </div>
          {estimatedUpload !== undefined && estimatedUpload > 20 && (
            <p role="note" className="text-sm">
              {t("ertmp-high-upload")}
            </p>
          )}
          <Button
            type="button"
            className="min-h-12"
            onClick={copy}
            disabled={!config}
          >
            <Clipboard className="size-4" aria-hidden />
            {t("ertmp-copy")}
          </Button>
        </div>
      </div>
      <details
        id={`${id}-obs`}
        className="group border-t border-(--color-border)"
      >
        <summary className={summaryClass}>
          {t("ertmp-obs-setup")}
          <ChevronDown
            className="size-4 shrink-0 group-open:rotate-180"
            aria-hidden
          />
        </summary>
        <ol className="list-decimal space-y-4 pt-3 pl-6 text-sm leading-relaxed">
          <li>
            <Trans
              ns="common"
              i18nKey="ertmp-step-server"
              components={{ strong: <strong className="font-semibold" /> }}
            />{" "}
            <a href="#stream-key" className="underline underline-offset-4">
              {t("ertmp-open-stream-key")}
            </a>
          </li>
          <li>
            <Trans
              ns="common"
              i18nKey="ertmp-step-enable"
              components={{ strong: <strong className="font-semibold" /> }}
            />
          </li>
          <li className="space-y-2">
            <p>{t("ertmp-step-media")}</p>
            <strong className="block font-semibold tabular-nums">
              {topQuality.width}×{resolution} · {frameRate} fps · 48 kHz
            </strong>
          </li>
        </ol>
      </details>
    </div>
  );
}
