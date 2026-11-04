export const ERTMP_ENCODERS = {
  x264: { h264: "obs_x264", av1: null },
  apple: { h264: "com.apple.videotoolbox.videoencoder.ave.avc", av1: null },
  nvidia: { h264: "obs_nvenc_h264_tex", av1: "obs_nvenc_av1_tex" },
  amd: { h264: "h264_texture_amf", av1: "av1_texture_amf" },
  intel: { h264: "obs_qsv11_v2", av1: "obs_qsv11_av1" },
} as const;

export type ERTMPEncoder = keyof typeof ERTMP_ENCODERS;

// Fixed upload budgets per quality. The 360p fallback stays at 30 fps.
export const ERTMP_QUALITIES = [
  { resolution: 1440, width: 2560, bitrate: { 30: 9000, 60: 10450 } },
  { resolution: 1080, width: 1920, bitrate: { 30: 5000, 60: 6000 } },
  { resolution: 720, width: 1280, bitrate: { 30: 3250, 60: 3750 } },
  { resolution: 360, width: 640, bitrate: { 30: 1000 } },
] as const;

export const ERTMP_AUDIO_BITRATE = 128;

interface ERTMPConfigOptions {
  encoder: ERTMPEncoder;
  resolution: 1080 | 1440;
  frameRate: 30 | 60;
}

// OBS 32 encoder settings: plugins/obs-nvenc, obs-ffmpeg/texture-amf.cpp,
// obs-qsv11, mac-videotoolbox and obs-x264 in obsproject/obs-studio.
const LOW_LATENCY_SETTINGS = {
  x264: { preset: "veryfast", tune: "zerolatency" },
  apple: { bframes: false },
  nvidia: {
    preset: "p5",
    tune: "ll",
    multipass: "disabled",
    bf: 0,
    lookahead: false,
  },
  amd: { preset: "balanced", bf: 0, pre_analysis: false },
  intel: { target_usage: "TU4", latency: "low", bframes: 0 },
} as const;

export function createERTMPConfig({
  encoder,
  resolution,
  frameRate,
}: ERTMPConfigOptions) {
  const qualities = ERTMP_QUALITIES.filter(
    (quality) => quality.resolution <= resolution,
  );
  if (resolution === 1440 && !ERTMP_ENCODERS[encoder].av1) return null;

  return {
    encoder_configurations: qualities.map(({ resolution, width, bitrate }) => ({
      type:
        resolution === 1440
          ? ERTMP_ENCODERS[encoder].av1!
          : ERTMP_ENCODERS[encoder].h264,
      width,
      height: resolution,
      framerate: {
        numerator: bitrate[frameRate] ? frameRate : 30,
        denominator: 1,
      },
      settings: {
        rate_control: "CBR",
        bitrate: bitrate[frameRate] ?? bitrate[30],
        keyint_sec: 1,
        profile: resolution === 1440 ? "main" : "high",
        ...LOW_LATENCY_SETTINGS[encoder],
      },
      canvas_index: 0,
    })),
    audio_configurations: {
      live: [
        {
          codec: "ffmpeg_aac",
          track_id: 1,
          channels: 2,
          settings: { bitrate: ERTMP_AUDIO_BITRATE },
        },
      ],
    },
  };
}
