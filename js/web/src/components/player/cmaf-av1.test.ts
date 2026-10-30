// @vitest-environment node
import { Hang } from "@moq/watch";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { expect, it } from "vitest";

const { Cmaf } = Hang.Container;
const source = new Uint8Array(
  readFileSync(
    resolve(
      import.meta.dirname,
      "../../../../../test/fixtures/ertmp-green-av1.mp4",
    ),
  ),
);
const sequence = Uint8Array.from([
  0x0a, 0x0a, 0x00, 0x00, 0x00, 0x03, 0xb4, 0xff, 0x73, 0x6b, 0xe4, 0x01,
]);
const init = Cmaf.decodeInitSegment(source);
const key = Cmaf.decodeDataSegment(source, init).find(
  (sample) => sample.keyframe,
)!;
const sequenceOffset = key.data.findIndex((_, offset) =>
  sequence.every((byte, index) => key.data[offset + index] === byte),
);
// The real fixture carries a sequence header in-band. RTMP sequence-start can
// move it to av1C, leaving a valid MP4 key sample without that repeated header.
expect(sequenceOffset).toBeGreaterThanOrEqual(0);
const withoutSequence = new Uint8Array(key.data.length - sequence.length);
withoutSequence.set(key.data.subarray(0, sequenceOffset));
withoutSequence.set(
  key.data.subarray(sequenceOffset + sequence.length),
  sequenceOffset,
);

it("restores AV1 initialization OBUs only for decode, preserving signed CMAF bytes and timing", () => {
  expect(init.description).toBeUndefined();
  expect(init.av1SequenceHeader).toEqual(sequence);
  const fragment = Cmaf.encodeDataSegment({
    kind: "video",
    data: withoutSequence,
    timestamp: init.timescale * 3,
    duration: init.timescale / 10,
    keyframe: true,
    sequence: 4,
    trackId: init.trackId,
  });
  const original = fragment.slice();
  const [decoded] = new Cmaf.Format(init).decode(fragment);
  expect(decoded.payload.subarray(0, sequence.length)).toEqual(sequence);
  expect(decoded.payload.subarray(sequence.length)).toEqual(withoutSequence);
  expect(decoded.timestamp).toBe(3_000_000);
  expect(decoded.duration).toBe(100_000);
  expect(decoded.keyframe).toBe(true);
  expect(fragment).toEqual(original);
});

it.each([
  { position: "after the temporal delimiter", data: key.data },
  {
    position: "at the start",
    data: Uint8Array.from([...sequence, ...withoutSequence]),
  },
])(
  "does not duplicate initialization already $position in an AV1 key sample",
  ({ data }) => {
    const fragment = Cmaf.encodeDataSegment({
      kind: "video",
      data,
      timestamp: 0,
      duration: init.timescale / 10,
      keyframe: true,
      sequence: 1,
    });
    expect(new Cmaf.Format(init).decode(fragment)[0].payload).toEqual(data);
  },
);

it("leaves AV1 delta payloads and other codecs unchanged", () => {
  const fragment = Cmaf.encodeDataSegment({
    kind: "video",
    data: withoutSequence,
    timestamp: 0,
    duration: init.timescale / 10,
    keyframe: false,
    sequence: 1,
  });
  expect(new Cmaf.Format(init).decode(fragment)[0].payload).toEqual(
    withoutSequence,
  );
  const h264 = new Uint8Array(
    readFileSync(
      resolve(
        import.meta.dirname,
        "../../../../../test/fixtures/ertmp-red-h264.mp4",
      ),
    ),
  );
  const h264Init = Cmaf.decodeInitSegment(h264);
  const h264Key = Cmaf.decodeDataSegment(h264, h264Init).find(
    (sample) => sample.keyframe,
  )!;
  const keyFragment = Cmaf.encodeDataSegment({
    kind: "video",
    data: h264Key.data,
    timestamp: 0,
    duration: h264Init.timescale / 10,
    keyframe: true,
    sequence: 1,
  });
  expect(new Cmaf.Format(h264Init).decode(keyFragment)[0].payload).toEqual(
    h264Key.data,
  );
});
