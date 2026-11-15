import { describe, expect, it } from "vitest";
import { audioStems, audioTrackGroup, audioTrackLevels } from "./audio-stems";

describe("received audio groups", () => {
  it("keeps alternate codecs together and selects two playable groups above the main mix", () => {
    expect(
      audioStems([
        { codec: "aac", alternateGroup: 1 },
        { codec: "opus", alternateGroup: 1 },
        { codec: "aac", alternateGroup: 2, label: "Unavailable" },
        { codec: "opus", alternateGroup: 5 },
        { codec: "aac", alternateGroup: 4, label: "Voice" },
        { codec: "opus", alternateGroup: 4 },
        { codec: "opus", alternateGroup: 3, label: "Music" },
      ]),
    ).toEqual([
      { alternateGroup: 3, label: "Music" },
      { alternateGroup: 4, label: "Voice" },
    ]);
    expect(audioStems([{ codec: "opus" }])).toEqual([]);
    expect(audioStems([])).toEqual([]);
  });

  it("does not route unknown tracks and rejects malformed group IDs", () => {
    expect(
      [
        "audio",
        "audio-g2",
        "audio-g0",
        "audio-g65536",
        "audio-g2x",
        "video",
      ].map(audioTrackGroup),
    ).toEqual([null, 2, undefined, undefined, undefined, undefined]);
    expect(
      audioStems([
        { codec: "opus" },
        { codec: "opus", alternateGroup: -1 },
        { codec: "opus", alternateGroup: 1.5 },
      ]),
    ).toEqual([]);
  });
});

describe("mix levels", () => {
  const stems = [
    { alternateGroup: 2, label: "Music" },
    { alternateGroup: 3, label: "Voice" },
  ];
  it("plays only the main mix by default or until every requested stem is available", () => {
    expect(audioTrackLevels([null, 2, 3], stems, null)).toEqual([1, 0, 0]);
    expect(audioTrackLevels([null, 2, undefined], stems, { 2: 0 })).toEqual([
      1, 0, 0,
    ]);
    expect(audioTrackLevels([null], [], { 2: 0 })).toEqual([1]);
  });
  it("silences main in custom mode and follows group identity rather than transceiver order", () => {
    expect(audioTrackLevels([3, null, 2], stems, { 2: 0.25 })).toEqual([
      1, 0, 0.25,
    ]);
    expect(audioTrackLevels([null, 2, 3], [stems[1]], { 3: 0.5 })).toEqual([
      0, 0, 0.5,
    ]);
    expect(audioTrackLevels([null, 2, 3], stems, { 2: 0, 3: 0 })).toEqual([
      0, 0, 0,
    ]);
  });
  it("bounds gain inputs and never sends nonfinite values into the audio graph", () => {
    expect(audioTrackLevels([null, 2, 3], stems, { 2: -1, 3: 9 })).toEqual([
      0, 0, 1,
    ]);
    expect(
      audioTrackLevels([null, 2, 3], stems, { 2: NaN, 3: Infinity }),
    ).toEqual([0, 1, 1]);
  });
});
