import { describe, expect, it } from "vitest";
import { getQualityLabels } from "./quality-labels";

describe("rendition quality labels", () => {
  it("preserves unique resolution and publisher labels", () => {
    expect(
      getQualityLabels([
        { label: "720p", codec: "avc1.64001f", id: "video1" },
        { label: "Main camera", codec: "av01.0.08M.08", id: "video2" },
      ]),
    ).toEqual(["720p", "Main camera"]);
  });

  it("distinguishes same-height H.264 and AV1 tracks", () => {
    expect(
      getQualityLabels([
        { label: "1080p", codec: "avc1.640028", id: "video17" },
        { label: "1080p", codec: "av01.0.08M.08", id: "video9" },
      ]),
    ).toEqual(["1080p (H.264, video17)", "1080p (AV1, video9)"]);
  });

  it("keeps same-height tracks with the same codec individually identifiable", () => {
    expect(
      getQualityLabels([
        { label: "720p", codec: "avc1.64001f", id: "video3" },
        { label: "720p", codec: "avc3.64001f", id: "video8" },
      ]),
    ).toEqual(["720p (H.264, video3)", "720p (H.264, video8)"]);
  });

  it("uses track identity when a backend has no codec metadata", () => {
    expect(
      getQualityLabels([
        { label: "720p", id: "track1" },
        { label: "720p", id: "track4" },
      ]),
    ).toEqual(["720p (track1)", "720p (track4)"]);
  });

  it("retains an unrecognized codec string instead of guessing its name", () => {
    expect(
      getQualityLabels([
        { label: "720p", codec: "vp09.00.10.08", id: "video1" },
        { label: "720p", codec: "vp09.02.10.10", id: "video2" },
      ]),
    ).toEqual(["720p (vp09.00.10.08, video1)", "720p (vp09.02.10.10, video2)"]);
  });

  it("does not add choices when no renditions are available", () => {
    expect(getQualityLabels([])).toEqual([]);
  });
});
