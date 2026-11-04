import { expect, it } from "vitest";
import { createERTMPConfig } from "./ertmp-config";

it.each([
  ["x264", "obs_x264", { preset: "veryfast", tune: "zerolatency" }],
  ["apple", "com.apple.videotoolbox.videoencoder.ave.avc", { bframes: false }],
  ["nvidia", "obs_nvenc_h264_tex", { bf: 0, lookahead: false, tune: "ll" }],
  ["amd", "h264_texture_amf", { bf: 0, pre_analysis: false }],
  ["intel", "obs_qsv11_v2", { bframes: 0, latency: "low" }],
] as const)(
  "generates low-latency H.264 settings for %s",
  (encoder, type, settings) => {
    const config = createERTMPConfig({
      encoder,
      frameRate: 60,
      resolution: 1080,
    })!;
    expect(config.encoder_configurations).toHaveLength(3);
    expect(config.encoder_configurations[0]).toEqual(
      expect.objectContaining({
        type,
        width: 1920,
        height: 1080,
        framerate: { numerator: 60, denominator: 1 },
        canvas_index: 0,
        settings: expect.objectContaining({
          ...settings,
          rate_control: "CBR",
          bitrate: 6000,
          keyint_sec: 1,
          profile: "high",
        }),
      }),
    );
    expect(config.audio_configurations.live[0].settings.bitrate).toBe(128);
  },
);

it.each([
  ["nvidia", "obs_nvenc_av1_tex", "obs_nvenc_h264_tex"],
  ["amd", "av1_texture_amf", "h264_texture_amf"],
  ["intel", "obs_qsv11_av1", "obs_qsv11_v2"],
] as const)(
  "keeps lower qualities H.264 when %s sends 1440p AV1",
  (encoder, av1, h264) => {
    const config = createERTMPConfig({
      encoder,
      frameRate: 30,
      resolution: 1440,
    })!;
    expect(
      config.encoder_configurations.map(({ type, width, height, settings }) => [
        type,
        width,
        height,
        settings.profile,
        settings.bitrate,
      ]),
    ).toEqual([
      [av1, 2560, 1440, "main", 9000],
      [h264, 1920, 1080, "high", 5000],
      [h264, 1280, 720, "high", 3250],
      [h264, 640, 360, "high", 1000],
    ]);
  },
);

it.each(["apple", "x264"] as const)(
  "does not generate unsupported AV1 for %s",
  (encoder) => {
    expect(
      createERTMPConfig({
        encoder,
        frameRate: 30,
        resolution: 1440,
      }),
    ).toBeNull();
  },
);

it("keeps the 360p fallback at 30 fps when the higher qualities use 60 fps", () => {
  const config = createERTMPConfig({
    encoder: "nvidia",
    frameRate: 60,
    resolution: 1440,
  })!;
  expect(
    config.encoder_configurations.map(({ height, framerate, settings }) => [
      height,
      framerate.numerator,
      settings.bitrate,
    ]),
  ).toEqual([
    [1440, 60, 10450],
    [1080, 60, 6000],
    [720, 60, 3750],
    [360, 30, 1000],
  ]);
});
