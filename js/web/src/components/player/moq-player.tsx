import type * as Watch from "@moq/watch";
import { useEffect, useImperativeHandle, useRef, type RefObject } from "react";
import { useTranslation } from "react-i18next";
import {
  getMoqPlaybackFloor,
  getMoqPlaybackSource,
  STALL_TIMEOUT_MS,
} from "./moq-source";
import type { PlayerBackendHandle, PlayerStats, QualityOption } from "./player";
import { getQualityLabels } from "./quality-labels";

type MoQPlayerProps = {
  canvasRef: RefObject<HTMLCanvasElement | null>;
  src: string;
  paused: boolean;
  muted: boolean;
  volume: number;
  onUnavailable: () => void;
  onPlaying: () => void;
  onBufferingChange: (buffering: boolean) => void;
  onQualitiesChange: (qualities: QualityOption[]) => void;
  onCurrentQualityChange: (index: number) => void;
  onStatsChange: (stats: PlayerStats) => void;
  ref?: RefObject<PlayerBackendHandle | null>;
};

type Controls = {
  paused: Watch.Signals.Signal<boolean>;
  muted: Watch.Signals.Signal<boolean>;
  volume: Watch.Signals.Signal<number>;
  target: Watch.Signals.Signal<Watch.Video.Target | undefined>;
};

export function MoQPlayer(props: MoQPlayerProps) {
  const { canvasRef, src, paused, muted, volume, ref } = props;
  const propsRef = useRef(props);
  propsRef.current = props;
  const { t } = useTranslation();
  const translationRef = useRef(t);
  translationRef.current = t;
  const controlsRef = useRef<Controls | null>(null);
  const renditionsRef = useRef<string[]>([]);

  useImperativeHandle(
    ref,
    () => ({
      setQuality(index) {
        const name = renditionsRef.current[index];
        if (index === -1 || name) {
          controlsRef.current?.target.set(name ? { name } : undefined);
        }
      },
    }),
    [],
  );

  useEffect(() => {
    controlsRef.current?.paused.set(paused);
    controlsRef.current?.muted.set(muted);
    controlsRef.current?.volume.set(volume);
  }, [paused, muted, volume]);

  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;
    const abort = new AbortController();
    let stopped = false;
    let player: Watch.Player | undefined;
    let connection: Watch.Net.Connection | undefined;
    let effects: Watch.Signals.Effect | undefined;
    let lastVideoProgress = Date.now();
    let lastAudioProgress = lastVideoProgress;
    let videoTimestamp: number | undefined;
    let audioTimestamp: number | undefined;

    function close() {
      stopped = true;
      abort.abort();
      clearInterval(watchdog);
      effects?.close();
      player?.close();
      connection?.close();
      controlsRef.current = null;
      renditionsRef.current = [];
    }

    function unavailable() {
      if (stopped) return;
      close();
      propsRef.current.onUnavailable();
    }

    // One clock covers both a hung startup and a decoder that stops advancing.
    const watchdog = setInterval(() => {
      const now = Date.now();
      const state = propsRef.current;
      const video = player?.renderer.out.timestamp.peek();
      const audio = player?.audio.out.timestamp.peek();
      const audioRenditions =
        player?.audio.source.out.catalog.peek()?.renditions;
      if (
        state.paused ||
        player?.renderer.out.visible.peek() === false ||
        (video !== undefined && video !== videoTimestamp)
      ) {
        lastVideoProgress = now;
      }
      if (
        state.paused ||
        state.muted ||
        !audioRenditions ||
        Object.keys(audioRenditions).length === 0 ||
        (audio !== undefined && audio !== audioTimestamp)
      ) {
        lastAudioProgress = now;
      }
      videoTimestamp = video;
      audioTimestamp = audio;
      if (
        now - lastVideoProgress >= STALL_TIMEOUT_MS ||
        now - lastAudioProgress >= STALL_TIMEOUT_MS
      ) {
        unavailable();
        return;
      }
      if (!player) return;
      const decoded = player.video.out.stats.peek();
      const frames = decoded?.frameCount ?? 0;
      const config = player.video.source.out.config.peek();
      const display = player.video.out.display.peek();
      const bufferedEnd = player.video.out.buffered.peek().at(-1)?.end;
      const target = controlsRef.current?.target.peek();
      state.onStatsChange({
        width: display?.width ?? 0,
        height: display?.height ?? 0,
        viewportWidth: window.innerWidth,
        viewportHeight: window.innerHeight,
        buffered:
          bufferedEnd === undefined || video === undefined
            ? 0
            : Math.max(0, bufferedEnd - video) / 1000,
        totalFrames: frames,
        renditionBitrate: config?.bitrate,
        availableBandwidth: connection?.probe.peek()?.estimatedRecvRate,
        level: target?.name ? renditionsRef.current.indexOf(target.name) : -1,
        codecs:
          [config?.codec, player.audio.source.out.config.peek()?.codec]
            .filter(Boolean)
            .join(", ") || undefined,
      });
    }, 1000);

    async function start() {
      try {
        const source = await getMoqPlaybackSource(src, abort.signal);
        if (stopped) return;
        if (!source) return unavailable();
        // The package creates browser media resources while importing it.
        const Watch = await import("@moq/watch");
        if (stopped) return;
        const controls: Controls = {
          paused: new Watch.Signals.Signal(propsRef.current.paused),
          muted: new Watch.Signals.Signal(true),
          volume: new Watch.Signals.Signal(propsRef.current.volume),
          target: new Watch.Signals.Signal<Watch.Video.Target | undefined>(
            undefined,
          ),
        };
        const currentConnection = (connection = new Watch.Net.Connection({
          url: source.url,
          discovery: false,
          websocket: { enabled: false },
          webtransport: source.certificateHash
            ? {
                serverCertificateHashes: [{ value: source.certificateHash }],
              }
            : undefined,
        }));
        const currentPlayer = (player = new Watch.Player({
          origin: currentConnection.origin,
          probe: currentConnection.probe,
          name: Watch.Net.Path.from(source.name),
          announced: false,
          catalogFormat: "hang",
          canvas: canvas ?? undefined,
          ...controls,
        }));
        effects = new Watch.Signals.Effect();
        const publicationJitter = new Watch.Signals.Signal<
          Watch.Net.Time.Milli | undefined
        >(undefined);
        effects.cleanup(currentPlayer.sync.register(publicationJitter));
        effects.run((effect) => {
          const catalog = effect.get(currentPlayer.broadcast.out.catalog);
          if (catalog === undefined) {
            publicationJitter.set(undefined);
            controlsRef.current = null;
            controls.muted.set(true);
            return;
          }
          const floor = getMoqPlaybackFloor(catalog, publicationJitter.peek());
          if (floor === undefined) return unavailable();
          const requiredDelay = Watch.Net.Time.Milli(floor);
          publicationJitter.set(requiredDelay);
          // Signals propagate asynchronously; wait for the SDK to size its buffer.
          const ready =
            effect.get(currentPlayer.sync.out.delay) >= requiredDelay;
          controlsRef.current = ready ? controls : null;
          controls.muted.set(!ready || propsRef.current.muted);
          if (ready) {
            controls.paused.set(propsRef.current.paused);
            controls.volume.set(propsRef.current.volume);
          }
        });
        let waitingToPlay = true;
        effects.cleanup(
          currentPlayer.renderer.out.timestamp.subscribe((timestamp) => {
            if (stopped || timestamp === undefined || propsRef.current.paused)
              return;
            if (waitingToPlay) {
              waitingToPlay = false;
              propsRef.current.onPlaying();
            }
          }),
        );
        effects.run((effect) => {
          const paused = effect.get(controls.paused);
          const stalled = effect.get(currentPlayer.video.out.stalled);
          const error = effect.get(currentConnection.error);
          const unsupported = effect.get(currentPlayer.video.source.out.error);
          if ((error || unsupported) && !paused) return unavailable();
          if (paused || stalled) waitingToPlay = true;
          propsRef.current.onBufferingChange(!paused && stalled);
        });
        effects.run((effect) => {
          const available = effect.get(
            currentPlayer.video.source.out.available,
          );
          const entries = Object.entries(available);
          renditionsRef.current = entries.map(([name]) => name);
          const labels = getQualityLabels(
            entries.map(([name, config]) => ({
              id: name,
              codec: config.codec,
              label:
                config.label ??
                (config.codedHeight ? `${config.codedHeight}p` : name),
            })),
          );
          propsRef.current.onQualitiesChange([
            { index: -1, label: translationRef.current("player-quality-auto") },
            ...labels.map((label, index) => ({
              index,
              label,
            })),
          ]);
          const target = effect.get(controls.target);
          propsRef.current.onCurrentQualityChange(
            target?.name ? renditionsRef.current.indexOf(target.name) : -1,
          );
        });
      } catch {
        if (!stopped) unavailable();
      }
    }
    void start();
    return close;
  }, [src, canvasRef]);

  return null;
}
