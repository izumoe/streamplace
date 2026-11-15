import { useId } from "react";
import { useTranslation } from "react-i18next";
import { Slider } from "../ui/slider";
import type { AudioMix, AudioStem } from "./audio-stems";

export function AudioMixControls({
  stems,
  mix,
  onMixChange,
  status,
}: {
  stems: AudioStem[];
  mix: AudioMix;
  onMixChange: (mix: AudioMix) => void;
  status?: "loading" | "unavailable";
}) {
  const { t } = useTranslation();
  const id = useId();
  return (
    <fieldset
      className="space-y-3 px-2 py-2"
      onKeyDown={(event) => {
        if (event.key === " ") event.stopPropagation();
      }}
    >
      <legend className="text-muted-foreground text-xs">
        {t("player-audio-mix")}
      </legend>
      <button
        type="button"
        aria-pressed={mix === null}
        onClick={() => onMixChange(null)}
        className="focus-visible:ring-ring hover:bg-accent aria-pressed:bg-accent min-h-11 w-full rounded-md px-2 text-left text-sm focus-visible:ring-2"
      >
        {t("player-audio-full-mix")}
      </button>
      {stems.map((stem) => (
        <div key={stem.alternateGroup} className="block space-y-2 text-sm">
          <span className="flex items-center justify-between gap-3">
            <span
              id={`${id}-${stem.alternateGroup}`}
              className="max-w-48 truncate"
            >
              {stem.label}
            </span>
            <span className="text-muted-foreground tabular-nums">
              {Math.round((mix?.[stem.alternateGroup] ?? 1) * 100)}%
            </span>
          </span>
          <Slider
            aria-labelledby={`${id}-${stem.alternateGroup}`}
            min={0}
            max={100}
            step={1}
            value={(mix?.[stem.alternateGroup] ?? 1) * 100}
            onValueChange={(value) =>
              onMixChange({
                ...mix,
                [stem.alternateGroup]:
                  (Array.isArray(value) ? value[0] : value) / 100,
              })
            }
          />
        </div>
      ))}
      {status && (
        <p role="status" className="text-muted-foreground text-xs">
          {t(
            status === "loading"
              ? "player-audio-mix-loading"
              : "player-audio-mix-unavailable",
          )}
        </p>
      )}
    </fieldset>
  );
}
