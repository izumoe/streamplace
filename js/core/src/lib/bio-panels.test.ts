import { describe, expect, it } from "vitest";
import { getBioPanelsLayout } from "./bio-panels";

describe("profile masonry", () => {
  it("places each card in the shortest column without leaving row gaps", () => {
    expect(getBioPanelsLayout(800, 376, 16, [300, 100, 150, 80])).toEqual({
      containerHeight: 362,
      positions: [
        { x: 16, y: 0 },
        { x: 408, y: 0 },
        { x: 408, y: 116 },
        { x: 408, y: 282 },
      ],
    });
  });

  it("caps wide profiles at five columns and centers their cards", () => {
    const layout = getBioPanelsLayout(
      3000,
      376,
      16,
      [100, 100, 100, 100, 100, 100],
    )!;
    expect(new Set(layout.positions.map(({ x }) => x)).size).toBe(5);
    expect(layout.positions[0]).toEqual({ x: 528, y: 0 });
    expect(layout.positions[5]).toEqual({ x: 528, y: 116 });
    expect(layout.containerHeight).toBe(216);
  });

  it.each([767, 768])(
    "adds a column only when it fits at %i pixels",
    (width) => {
      const layout = getBioPanelsLayout(width, 376, 16, [100, 100])!;
      expect(new Set(layout.positions.map(({ x }) => x)).size).toBe(
        width === 768 ? 2 : 1,
      );
    },
  );

  it("stacks cards in order on mobile and stays inside the available width", () => {
    expect(getBioPanelsLayout(320, 376, 16, [300, 100])).toEqual({
      containerHeight: 416,
      positions: [
        { x: 0, y: 0 },
        { x: 0, y: 316 },
      ],
    });
  });

  it("centers a single card without reserving empty columns", () => {
    expect(getBioPanelsLayout(1200, 376, 16, [100])!.positions).toEqual([
      { x: 412, y: 0 },
    ]);
  });

  it("keeps editors in their columns while typing and pushes later cards down", () => {
    const initial = getBioPanelsLayout(800, 376, 16, [300, 100, 150, 80])!;
    const editing = getBioPanelsLayout(
      800,
      376,
      16,
      [300, 500, 150, 80],
      initial.positions,
    )!;
    expect(editing.positions).toEqual([
      { x: 16, y: 0 },
      { x: 408, y: 0 },
      { x: 408, y: 516 },
      { x: 408, y: 682 },
    ]);
    expect(editing.containerHeight).toBe(762);
    expect(
      getBioPanelsLayout(800, 376, 16, [300, 500, 150, 80])!.positions[2],
    ).toEqual({ x: 16, y: 316 });
  });

  it("waits for measurements and handles an empty profile", () => {
    expect(getBioPanelsLayout(0, 376, 16, [100])).toBeNull();
    expect(getBioPanelsLayout(800, 376, 16, [100, null])).toBeNull();
    expect(getBioPanelsLayout(800, 376, 16, [])).toEqual({
      containerHeight: 0,
      positions: [],
    });
  });
});
