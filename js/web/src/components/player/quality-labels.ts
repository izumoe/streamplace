export function getQualityLabels(
  renditions: { label: string; codec?: string; id: string }[],
): string[] {
  const counts = new Map<string, number>();
  for (const { label } of renditions) {
    counts.set(label, (counts.get(label) ?? 0) + 1);
  }
  return renditions.map(({ label, codec, id }) => {
    if (counts.get(label) === 1) return label;
    const name =
      codec?.startsWith("avc1.") || codec?.startsWith("avc3.")
        ? "H.264"
        : codec?.startsWith("av01.")
          ? "AV1"
          : codec;
    return name ? `${label} (${name}, ${id})` : `${label} (${id})`;
  });
}
