export function getBioPanelsLayout(
  containerWidth: number,
  cardWidth: number,
  gap: number,
  heights: readonly (number | null)[],
  previousPositions?: readonly { x: number }[],
) {
  if (containerWidth <= 0 || heights.some((height) => height === null))
    return null;

  const columnWidth = Math.min(cardWidth, containerWidth);
  const columns = Math.max(
    1,
    Math.min(
      5,
      heights.length,
      Math.floor((containerWidth + gap) / (columnWidth + gap)),
    ),
  );
  const columnTops = new Array<number>(columns).fill(0);
  const offset =
    (containerWidth - columns * columnWidth - (columns - 1) * gap) / 2;
  const positions = heights.map((height, index) => {
    const previous = previousPositions?.[index];
    const previousColumn = previous
      ? Math.round((previous.x - offset) / (columnWidth + gap))
      : -1;
    const column =
      previousColumn >= 0 && previousColumn < columns
        ? previousColumn
        : columnTops.indexOf(Math.min(...columnTops));
    const position = {
      x: offset + column * (columnWidth + gap),
      y: columnTops[column],
    };
    columnTops[column] += height! + gap;
    return position;
  });
  return {
    positions,
    containerHeight: Math.max(0, Math.max(...columnTops) - gap),
  };
}
