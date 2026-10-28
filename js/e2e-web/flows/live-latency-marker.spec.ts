import { expect, test } from "@playwright/test";
import { makeLatencyMarker } from "./live-latency-marker";

const marker = makeLatencyMarker();
const brightness = (bits: number[]): number[] =>
  bits.map((bit) => (bit ? 232 : 24));

test("latency marker: IDs survive compression noise and unsigned boundaries", () => {
  for (const id of [0, 1, 65535, 65536, 0x80000000, 0xffffffff]) {
    expect(marker.encode(id)).toHaveLength(64);
    expect(marker.decode(brightness(marker.encode(id)))).toBe(id);
  }
});

test("latency marker: damaged header, ID or checksum is never a sample", () => {
  const pixels = brightness(marker.encode(0x12345678));
  expect(pixels).toHaveLength(64);
  for (let bit = 0; bit < pixels.length; bit++) {
    const damaged = [...pixels];
    damaged[bit] = 255 - damaged[bit];
    expect(marker.decode(damaged), `damaged bit ${bit}`).toBeNull();
  }
});

test("latency marker: uncertain and incomplete pixels are rejected", () => {
  const pixels = brightness(marker.encode(42));
  expect(marker.decode(pixels.slice(1))).toBeNull();
  for (const value of [128, -1, 256, NaN, Infinity]) {
    const damaged = [...pixels];
    damaged[20] = value;
    expect(marker.decode(damaged)).toBeNull();
  }
});

test("latency marker: invalid frame IDs fail before producing pixels", () => {
  for (const id of [-1, 0x100000000, 1.5, NaN, Infinity]) {
    expect(() => marker.encode(id)).toThrow(RangeError);
  }
});
