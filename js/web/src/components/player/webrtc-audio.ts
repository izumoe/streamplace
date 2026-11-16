import {
  audioTrackGroup,
  audioTrackLevels,
  type AudioMix,
  type AudioStem,
  type AudioTrackGroup,
} from "./audio-stems";

// Some browsers replace track IDs; the answer's msid still identifies each MID.
export function inboundAudioGroup(
  id: string,
  mid: string | null,
  sdp: string,
): AudioTrackGroup {
  const direct = audioTrackGroup(id);
  if (direct !== undefined) return direct;
  if (mid === null) return;
  for (const section of sdp.split(/(?=^m=)/m)) {
    if (!section.startsWith("m=audio ")) continue;
    if (!section.split(/\r?\n/).includes(`a=mid:${mid}`)) continue;
    const match = /^a=(?:msid:|ssrc:\d+ msid:)\S+ (\S+)/m.exec(section);
    return match ? audioTrackGroup(match[1]) : undefined;
  }
}

type InboundProgress = {
  id: string;
  kind?: string;
  mediaType?: string;
  framesDecoded?: number;
  framesReceived?: number;
  packetsReceived?: number;
  lastPacketReceivedTimestamp?: number;
};

export class WebRTCMediaProgress {
  #streams = new Map<string, { count: number; changed: number }>();
  constructor(private threshold: number) {}

  stalled(reports: InboundProgress[], now: number) {
    for (const report of reports) {
      const kind = report.mediaType ?? report.kind;
      const count =
        kind === "video"
          ? (report.framesDecoded ?? report.framesReceived)
          : kind === "audio"
            ? (report.packetsReceived ?? report.lastPacketReceivedTimestamp)
            : undefined;
      if (count === undefined || !Number.isFinite(count) || count < 0) continue;
      if (
        count === 0 &&
        !(kind === "video" && (report.framesReceived ?? 0) > 0)
      )
        continue;
      const previous = this.#streams.get(report.id);
      if (!previous || previous.count !== count) {
        this.#streams.set(report.id, { count, changed: now });
      }
    }
    for (const id of this.#streams.keys()) {
      if (!reports.some((report) => report.id === id)) this.#streams.delete(id);
    }
    for (const stream of this.#streams.values()) {
      if (now - stream.changed > this.threshold) return true;
    }
    return false;
  }
}

type AudioInput = {
  track: MediaStreamTrack;
  ended: () => void;
  source?: MediaStreamAudioSourceNode;
  gain?: GainNode;
};

// Keep native full-mix playback until a shared-clock stem graph is running.
export class WebRTCAudio {
  #inputs = new Map<number | null, AudioInput>();
  #context?: AudioContext;
  #master?: GainNode;
  #playout?: HTMLAudioElement;
  #failed = false;
  #closed = false;
  #stems: AudioStem[] = [];
  #mix: AudioMix = null;
  #reported = "";
  #gestureTarget: HTMLElement | HTMLVideoElement;

  constructor(
    private video: HTMLVideoElement,
    private output: MediaStream,
    private onStems: (stems: AudioStem[]) => void,
  ) {
    this.#gestureTarget = video.parentElement ?? video;
    video.addEventListener("volumechange", this.#sync);
    video.addEventListener("play", this.#resume);
    video.addEventListener("pause", this.#sync);
    this.#gestureTarget.addEventListener("pointerdown", this.#resume);
    this.#gestureTarget.addEventListener("keydown", this.#resume);
  }

  setMix(stems: AudioStem[], mix: AudioMix) {
    this.#stems = stems;
    this.#mix = mix;
    this.#update();
  }

  add(track: MediaStreamTrack, group: AudioTrackGroup) {
    if (this.#closed || group === undefined || track.kind !== "audio") return;
    if (this.#inputs.get(group)?.track === track) return;
    this.#remove(group);
    const ended = () => {
      this.#remove(group);
      this.#update();
    };
    track.addEventListener("ended", ended);
    const input = { track, ended };
    this.#inputs.set(group, input);
    if (this.#context) this.#connect(input);
    this.#update();
  }

  #remove(group: number | null) {
    const previous = this.#inputs.get(group);
    if (!previous) return;
    previous.track.removeEventListener("ended", previous.ended);
    previous.source?.disconnect();
    previous.gain?.disconnect();
    this.#inputs.delete(group);
  }

  #connect(input: AudioInput) {
    if (!this.#context || !this.#master) return;
    input.source = this.#context.createMediaStreamSource(
      new MediaStream([input.track]),
    );
    input.gain = this.#context.createGain();
    input.gain.gain.value = 0;
    input.source.connect(input.gain).connect(this.#master);
  }

  #update() {
    if (this.#closed) return;
    const available = this.#stems.filter(
      (stem) =>
        this.#inputs.get(stem.alternateGroup)?.track.readyState === "live",
    );
    const complete =
      available.length > 0 &&
      available.length === this.#stems.length &&
      this.#inputs.get(null)?.track.readyState === "live";
    if (!this.#context && !this.#failed && complete && this.#mix !== null) {
      try {
        this.#context = new AudioContext({ latencyHint: "interactive" });
        this.#master = this.#context.createGain();
        this.#master.gain.value = 0;
        this.#master.connect(this.#context.destination);
        this.#context.addEventListener("statechange", this.#sync);
        for (const input of this.#inputs.values()) this.#connect(input);
        // Chromium needs a playing media sink to decode remote WebRTC audio.
        this.#playout = document.createElement("audio");
        this.#playout.muted = true;
      } catch {
        this.#failed = true;
        this.#context?.close().catch(() => {});
        this.#context = undefined;
        this.#closePlayout();
      }
    }
    const main = this.#inputs.get(null)?.track;
    if (
      this.#playout &&
      main &&
      (this.#playout.srcObject as MediaStream | null)?.getAudioTracks()[0] !==
        main
    ) {
      this.#playout.srcObject = new MediaStream([main]);
      this.#resume();
    } else if (this.#playout && !main) {
      this.#playout.pause();
      this.#playout.srcObject = null;
    }
    const usable = this.#failed || !complete ? [] : available;
    const key = JSON.stringify(usable);
    if (key !== this.#reported) {
      this.#reported = key;
      this.onStems(usable);
    }
    const levels = audioTrackLevels(
      [...this.#inputs.keys()],
      this.#stems,
      this.#mix,
    );
    const now = this.#context?.currentTime ?? 0;
    [...this.#inputs.values()].forEach((input, index) => {
      const gain = input.gain?.gain;
      if (!gain) return;
      gain.cancelScheduledValues(now);
      gain.setValueAtTime(gain.value, now);
      gain.linearRampToValueAtTime(levels[index], now + 0.02);
    });
    this.#sync();
  }

  #resume = () => {
    if (!this.#closed && this.#playout?.paused) {
      this.#playout.play().catch(() => {});
    }
    if (!this.#closed && this.#context?.state === "suspended") {
      this.#context
        .resume()
        .then(this.#sync)
        .catch(() => {});
    }
    this.#sync();
  };

  #sync = () => {
    if (this.#closed) return;
    const routed = this.#context?.state === "running";
    const main = this.#inputs.get(null)?.track;
    const native = !routed && main?.readyState === "live" ? main : undefined;
    for (const track of this.output.getAudioTracks()) {
      if (track !== native) this.output.removeTrack(track);
    }
    if (native && !this.output.getAudioTracks().includes(native))
      this.output.addTrack(native);
    if (this.#master) {
      this.#master.gain.value =
        this.video.muted || this.video.paused ? 0 : this.video.volume;
    }
  };

  #closePlayout() {
    this.#playout?.pause();
    if (this.#playout) this.#playout.srcObject = null;
    this.#playout = undefined;
  }

  close() {
    if (this.#closed) return;
    this.#closed = true;
    this.video.removeEventListener("volumechange", this.#sync);
    this.video.removeEventListener("play", this.#resume);
    this.video.removeEventListener("pause", this.#sync);
    this.#gestureTarget.removeEventListener("pointerdown", this.#resume);
    this.#gestureTarget.removeEventListener("keydown", this.#resume);
    for (const group of this.#inputs.keys()) this.#remove(group);
    for (const track of this.output.getAudioTracks())
      this.output.removeTrack(track);
    this.#master?.disconnect();
    this.#closePlayout();
    this.#context?.removeEventListener("statechange", this.#sync);
    this.#context?.close().catch(() => {});
  }
}
