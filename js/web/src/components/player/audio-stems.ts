export type AudioStem = { alternateGroup: number; label: string };
export type AudioTrackGroup = number | null | undefined;
export type AudioMix = Record<number, number> | null;
type AudioTrack = { codec: string; alternateGroup?: number; label?: string };

function validGroup(group: number) {
  return Number.isInteger(group) && group >= 0 && group <= 65535;
}

// Codecs in a group are alternatives. The lowest group carries the full mix.
export function audioStems(audio: readonly AudioTrack[]): AudioStem[] {
  const valid = audio.filter((track) => validGroup(track.alternateGroup ?? 0));
  const main = Math.min(...valid.map((track) => track.alternateGroup ?? 0));
  const groups = new Map<number, string>();
  for (const track of valid) {
    const group = track.alternateGroup ?? 0;
    if (track.codec === "opus" && group > main) groups.set(group, "");
  }
  for (const track of valid) {
    const group = track.alternateGroup ?? 0;
    if (groups.has(group) && track.label && !groups.get(group)) {
      groups.set(group, track.label);
    }
  }
  return [...groups]
    .sort(([a], [b]) => a - b)
    .slice(0, 2)
    .map(([alternateGroup, label]) => ({
      alternateGroup,
      label: label || `Track ${alternateGroup}`,
    }));
}

export function audioTrackGroup(id: string): AudioTrackGroup {
  if (id === "audio") return null;
  const match = /^audio-g([1-9]\d*)$/.exec(id);
  const group = match ? Number(match[1]) : NaN;
  return validGroup(group) ? group : undefined;
}

// Incomplete custom mixes stay on main rather than silently losing a component.
export function audioTrackLevels(
  groups: AudioTrackGroup[],
  stems: AudioStem[],
  mix: AudioMix,
): number[] {
  const mixing =
    mix !== null &&
    stems.length > 0 &&
    stems.every((stem) => groups.includes(stem.alternateGroup));
  return groups.map((group) => {
    if (group === null) return mixing ? 0 : 1;
    if (
      !mixing ||
      group === undefined ||
      !stems.some((stem) => stem.alternateGroup === group)
    )
      return 0;
    const level = mix?.[group] ?? 1;
    return Number.isFinite(level) ? Math.min(1, Math.max(0, level)) : 1;
  });
}
