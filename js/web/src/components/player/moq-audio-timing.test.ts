// @vitest-environment node
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { dirname, join } from "node:path";
import { runInNewContext } from "node:vm";
import { describe, expect, it } from "vitest";

type Sample = {
  timestamp: number;
  sampleRate: number;
  numberOfFrames: number;
};

type Terminal = {
  span(sample: Sample): {
    timestamp: number;
    frameOffset: number;
    frames: number;
  };
  clear(preSkip?: number): void;
  update(next: {
    discontinuity: number;
    group: number;
    frame?: { timestamp: number };
    end?: number;
  }): boolean;
};

// Exercise the actual pinned SDK bundle, including the temporary package patch.
const entry = createRequire(import.meta.url).resolve("@moq/watch");
const index = readFileSync(entry, "utf8");
const player = index.match(/from "\.\/(player-[^"]+\.js)"/)?.[1];
if (!player) throw new Error("MoQ player bundle not found");
const bundle = readFileSync(join(dirname(entry), player), "utf8");
const start = bundle.indexOf("Ce = class {");
const end = bundle.indexOf("}, we = class {", start);
if (start < 0 || end < 0) throw new Error("MoQ audio terminal not found");
const AudioTerminal = runInNewContext(
  `(${bundle.slice(start + "Ce = ".length, end + 1)})`,
) as new () => Terminal;

function sample(timestamp: number, sampleRate = 48_000): Sample {
  return { timestamp, sampleRate, numberOfFrames: 1024 };
}

describe("MoQ decoded audio timing", () => {
  it("keeps millisecond-rounded AAC output contiguous without accumulating drift", () => {
    const terminal = new AudioTerminal();
    for (let i = 0; i < 3000; i++) {
      const exact = (i * 1024 * 1_000_000) / 48_000;
      const rounded = Math.round(exact / 1000) * 1000;
      expect(terminal.span(sample(rounded)).timestamp).toBe(Math.round(exact));
    }
  });

  it("preserves a real gap when timestamps have finer precision", () => {
    const terminal = new AudioTerminal();
    terminal.span(sample(0));
    expect(terminal.span(sample(21_833)).timestamp).toBe(21_833);
  });

  it("preserves a gap larger than timestamp rounding", () => {
    const terminal = new AudioTerminal();
    terminal.span(sample(0));
    expect(terminal.span(sample(25_000)).timestamp).toBe(25_000);
  });

  it("keeps rounded 44.1 kHz audio continuous", () => {
    const terminal = new AudioTerminal();
    for (let i = 0; i < 200; i++) {
      const exact = (i * 1024 * 1_000_000) / 44_100;
      const rounded = Math.round(exact / 1000) * 1000;
      expect(terminal.span(sample(rounded, 44_100)).timestamp).toBe(
        Math.round(exact),
      );
    }
  });

  it("preserves a one-millisecond gap in integral-duration packets", () => {
    const terminal = new AudioTerminal();
    terminal.span({ timestamp: 0, sampleRate: 48_000, numberOfFrames: 960 });
    expect(
      terminal.span({
        timestamp: 21_000,
        sampleRate: 48_000,
        numberOfFrames: 960,
      }).timestamp,
    ).toBe(21_000);
  });

  it("still trims decoded audio at the source endpoint", () => {
    const terminal = new AudioTerminal();
    terminal.span(sample(0));
    terminal.span(sample(21_000));
    terminal.update({ discontinuity: 0, group: 1, end: 60_000 });
    expect(terminal.span(sample(43_000))).toEqual({
      timestamp: 42_667,
      frameOffset: 0,
      frames: 832,
    });
  });

  it("reanchors after a discontinuity and a sample-rate change", () => {
    const terminal = new AudioTerminal();
    terminal.span(sample(0));
    terminal.span(sample(21_000));
    terminal.update({
      discontinuity: 1,
      group: 1,
      frame: { timestamp: 43_000 },
    });
    expect(terminal.span(sample(43_000)).timestamp).toBe(43_000);
    expect(terminal.span(sample(66_000, 44_100)).timestamp).toBe(66_000);
  });
});
