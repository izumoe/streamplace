import { describe, expect, it } from "vitest";
import {
  inboundAudioGroup,
  WebRTCAudio,
  WebRTCMediaProgress,
} from "./webrtc-audio";

const answer = [
  "v=0",
  "m=video 9 UDP/TLS/RTP/SAVPF 96",
  "a=mid:v",
  "a=msid:stream video",
  "m=audio 9 UDP/TLS/RTP/SAVPF 111",
  "a=mid:main",
  "a=msid:stream audio",
  "m=audio 9 UDP/TLS/RTP/SAVPF 111",
  "a=mid:music",
  "a=msid:stream audio-g3",
  "m=audio 9 UDP/TLS/RTP/SAVPF 111",
  "a=mid:other",
  "a=ssrc:42 msid:stream audio-g7",
].join("\r\n");

it("does not offer custom controls while a declared stem or the main mix is missing", () => {
  const reports: unknown[] = [];
  const video = document.createElement("video");
  const output = { getAudioTracks: () => [] } as unknown as MediaStream;
  const audio = new WebRTCAudio(video, output, (stems) => reports.push(stems));
  const voice = Object.assign(new EventTarget(), {
    kind: "audio",
    readyState: "live",
  }) as unknown as MediaStreamTrack;
  try {
    audio.setMix(
      [
        { alternateGroup: 2, label: "Voice" },
        { alternateGroup: 3, label: "Music" },
      ],
      null,
    );
    audio.add(voice, 2);
    expect(reports.at(-1)).toEqual([]);
    voice.dispatchEvent(new Event("ended"));
    expect(reports.at(-1)).toEqual([]);
  } finally {
    audio.close();
  }
});

describe("WebRTC audio identity", () => {
  it("uses stable IDs directly, or the matching audio MID from the answer", () => {
    expect(inboundAudioGroup("audio", null, answer)).toBeNull();
    expect(inboundAudioGroup("audio-g3", null, answer)).toBe(3);
    expect(inboundAudioGroup("random-browser-id", "main", answer)).toBeNull();
    expect(inboundAudioGroup("random-browser-id", "music", answer)).toBe(3);
    expect(inboundAudioGroup("random-browser-id", "other", answer)).toBe(7);
  });

  it("never infers identity from track order, another MID, or a video section", () => {
    expect(inboundAudioGroup("unknown", null, answer)).toBeUndefined();
    expect(inboundAudioGroup("unknown", "absent", answer)).toBeUndefined();
    expect(inboundAudioGroup("unknown", "v", answer)).toBeUndefined();
    expect(inboundAudioGroup("audio-g65536", null, answer)).toBeUndefined();
  });
});

describe("per-stream WebRTC progress", () => {
  it("does not let progressing audio conceal a frozen video stream", () => {
    const progress = new WebRTCMediaProgress(2000);
    expect(
      progress.stalled(
        [
          { id: "v", kind: "video", framesDecoded: 10 },
          { id: "a", kind: "audio", packetsReceived: 50 },
        ],
        0,
      ),
    ).toBe(false);
    expect(
      progress.stalled(
        [
          { id: "v", kind: "video", framesDecoded: 10 },
          { id: "a", kind: "audio", packetsReceived: 100 },
        ],
        2001,
      ),
    ).toBe(true);
  });

  it("detects a decoder that never outputs a frame after video packets arrive", () => {
    const progress = new WebRTCMediaProgress(2000);
    expect(
      progress.stalled(
        [{ id: "v", kind: "video", framesReceived: 10, framesDecoded: 0 }],
        0,
      ),
    ).toBe(false);
    expect(
      progress.stalled(
        [{ id: "v", kind: "video", framesReceived: 40, framesDecoded: 0 }],
        2001,
      ),
    ).toBe(true);
  });

  it("tracks audio independently and tolerates unused recvonly transceivers", () => {
    const progress = new WebRTCMediaProgress(2000);
    expect(
      progress.stalled(
        [
          { id: "main", kind: "audio", packetsReceived: 50 },
          { id: "stem", kind: "audio", packetsReceived: 40 },
          { id: "unused", kind: "audio", packetsReceived: 0 },
        ],
        10,
      ),
    ).toBe(false);
    expect(
      progress.stalled(
        [
          { id: "main", kind: "audio", packetsReceived: 100 },
          { id: "stem", kind: "audio", packetsReceived: 40 },
        ],
        2011,
      ),
    ).toBe(true);
  });

  it("retires inbound reports removed by renegotiation", () => {
    const progress = new WebRTCMediaProgress(2000);
    progress.stalled([{ id: "old", kind: "video", framesDecoded: 10 }], 0);
    expect(
      progress.stalled([{ id: "new", kind: "video", framesDecoded: 11 }], 2001),
    ).toBe(false);
  });

  it("resets a replaced counter and falls back to older browser counters", () => {
    const progress = new WebRTCMediaProgress(2000);
    progress.stalled([{ id: "v", kind: "video", framesReceived: 10 }], 0);
    expect(
      progress.stalled([{ id: "v", kind: "video", framesReceived: 1 }], 2010),
    ).toBe(false);
    expect(
      progress.stalled([{ id: "v", kind: "video", framesReceived: 2 }], 4010),
    ).toBe(false);
  });
});
