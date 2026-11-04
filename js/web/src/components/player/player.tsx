import { useFullscreen } from "@/contexts/fullscreen-context";
import { useSonare } from "@/lib/useSonare";
import { CatchBoundary } from "@tanstack/react-router";
import {
  lazy,
  Suspense,
  useCallback,
  useEffect,
  useRef,
  useState,
  type ReactNode,
  type RefObject,
} from "react";
import { useTranslation } from "react-i18next";
import { cn } from "../../lib/utils";
import { Button } from "../ui/button";
import { Loader } from "../ui/loader";
import { ContentWarningOverlay } from "./content-warning-overlay";
import { HLSPlayer } from "./hls-player";
import { MoQPlayer } from "./moq-player";
import { supportsMoqPlayback } from "./moq-source";
import {
  getBufferingOverlayPresentation,
  getBufferingState,
  shouldShowBufferingIndicator,
  type BufferingMediaEvent,
} from "./player-buffering";
import { PlayerControls } from "./player-controls";
import { WebRTCPlayer } from "./webrtc-player";

const StatsOverlay = lazy(() => import("./stats-overlay"));

export type PlayerProps = {
  /** Full URL to the media source. Scheme/route is dispatched to a backend. */
  src: string;
  /** Live streams hide the scrubber; VODs get a seek bar. */
  mode?: "live" | "vod";
  /**
   * Seconds into a VOD to start playback at, from a `?t=` URL param.
   * Ignored for live playback and for values past the media's end.
   */
  startTime?: number;
  /**
   * Prefer the low-latency live HLS preset. Off by default: standard
   * latency holds more buffer and is less prone to rebuffering.
   */
  lowLatency?: boolean;
  /** Optional poster image (e.g. /api/playback/:user/stream.jpg). */
  poster?: string;
  /** Optional poster that overrides the default when the stream is offline. */
  fallbackPoster?: string;
  /** True when playback should be active. When false we just render the poster. */
  active: boolean;
  /**
   * Called when the player hits a fatal error. The parent may surface
   * this as a `LivestreamProblem` in the store.
   */
  onError?: (message: string) => void;
  /** Called once playback has actually started. */
  onPlaying?: () => void;
  /** Whether the danmu overlay is visible. */
  showDanmu?: boolean;
  /** Toggle the danmu overlay. */
  onShowDanmuChange?: (show: boolean) => void;
  /**
   * Overlay node rendered inside the player container so it
   * travels with the video into fullscreen.
   */
  overlay?: ReactNode;
  /** Content-warning labels associated with the current video or stream. */
  contentWarnings?: string[];
  /** Whether the current user is the stream owner. */
  isStreamer?: boolean;
};

/** One quality option shown in the player's settings menu. */
export type QualityOption = {
  /** Backend-specific index. hls.js uses -1 for "auto". */
  index: number;
  /** Human-readable label, e.g. "Auto", "1080p", "720p". */
  label: string;
};

/**
 * Snapshot of player stats pushed roughly once a second. The shape is
 * shared by all backends; fields are optional when a backend doesn't
 * supply them (e.g. WebRTC won't have hls.js's bitrate).
 */
export type PlayerStats = {
  /** Current decoded video width. */
  width: number;
  /** Current decoded video height. */
  height: number;
  /** Current browser viewport width. */
  viewportWidth: number;
  /** Current browser viewport height. */
  viewportHeight: number;
  /** Seconds of buffer ahead of the playhead. */
  buffered: number;
  /** Decoded-and-dropped video frames since load. */
  droppedFrames?: number;
  /** Total decoded video frames since load. */
  totalFrames: number;
  /** Computed from frame deltas (current interval, fps). */
  fps?: number;
  /** Current stream bitrate in bits per second. */
  bitrate?: number;
  /** Selected video rendition's conservative catalog estimate, in bits per second. */
  renditionBitrate?: number;
  /** Connection's estimated available bandwidth, in bits per second. */
  availableBandwidth?: number;
  /** hls.js's time-to-first-byte estimate in milliseconds (HLS-specific). */
  ttfbEstimate?: number;
  /** Current level index (-1 for auto in HLS). */
  level: number;
  /** Comma-separated codec strings for the current audio and video. */
  codecs?: string;
  /** hls.js package version, e.g. "1.5.17" (HLS-specific). */
  hlsVersion?: string;
  /** Estimated seconds behind the receiver's live edge; excludes capture/ingest delay. */
  liveEdgeDelay?: number;
  /** Receiver's configured playback delay, including buffering. */
  playbackDelayMs?: number;
  /** Signed media duration published in a burst, not time spent processing it. */
  publicationBatchMs?: number;
  /** Round-trip time to the connected relay. */
  roundTripTimeMs?: number;
  /** Video media timestamp minus audio playout timestamp (positive means video ahead). */
  avSyncMs?: number;
  /** WebRTC averages since the inbound video stream started, in milliseconds. */
  processingDelayMs?: number;
  decodeTimeMs?: number;
  jitterBufferMs?: number;
};

/**
 * Imperative handle the chrome holds for the active backend. Backends
 * implement whichever operations their transport supports. HLS exposes
 * `setQuality`; future WebRTC may expose a no-op or a different model.
 */
export type PlayerBackendHandle = {
  setQuality: (index: number) => void;
};

const IDLE_HIDE_MS = 3000;
const TRANSPORT_KEY = "player-transport";

function readTransportPreference(): boolean {
  try {
    const v = localStorage.getItem(TRANSPORT_KEY);
    if (v === "hls") return false;
    if (v === "webrtc") return true;
  } catch {
    // localStorage unavailable (SSR, private browsing, etc.)
  }
  return true;
}

function writeTransportPreference(useWebRTC: boolean) {
  try {
    localStorage.setItem(TRANSPORT_KEY, useWebRTC ? "webrtc" : "hls");
  } catch {
    // ignore
  }
}

const QUALITY_KEY = "player-quality";

export function readQualityPreference(): number | null {
  try {
    const v = localStorage.getItem(QUALITY_KEY);
    if (v !== null) return parseInt(v, 10);
  } catch {
    // localStorage unavailable
  }
  return null;
}

function writeQualityPreference(index: number) {
  try {
    localStorage.setItem(QUALITY_KEY, String(index));
  } catch {
    // ignore
  }
}

export function Player({
  src,
  mode = "live",
  startTime,
  lowLatency = false,
  poster,
  fallbackPoster,
  active,
  onError,
  onPlaying,
  showDanmu = false,
  onShowDanmuChange,
  overlay,
  contentWarnings = [],
  isStreamer,
}: PlayerProps) {
  const videoRef = useRef<HTMLVideoElement | null>(null);
  const canvasRef = useRef<HTMLCanvasElement | null>(null);
  const containerRef = useRef<HTMLDivElement | null>(null);
  const { fullscreen } = useFullscreen();
  const backendRef = useRef<PlayerBackendHandle | null>(null);
  const idleTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const { t } = useTranslation();

  const [playing, setPlaying] = useState(false);
  const [buffering, setBuffering] = useState(active);
  const [hasPlayed, setHasPlayed] = useState(false);
  const [manifestReady, setManifestReady] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [showControls, setShowControls] = useState(true);
  const [qualities, setQualities] = useState<QualityOption[]>([]);
  const [currentQuality, setCurrentQuality] = useState<number>(-1);
  const [useWebRTC, setUseWebRTC] = useState(readTransportPreference);
  const [moqUnavailableFor, setMoqUnavailableFor] = useState<string | null>(
    null,
  );
  const [moqPaused, setMoqPaused] = useState(false);
  const [moqMuted, setMoqMuted] = useState(true);
  const [moqVolume, setMoqVolume] = useState(1);
  const transport =
    mode === "vod"
      ? "hls"
      : src.startsWith("webrtc://")
        ? "webrtc"
        : !useWebRTC
          ? "hls"
          : supportsMoqPlayback() && moqUnavailableFor !== src
            ? "moq"
            : "webrtc";
  const [showStats, setShowStats] = useState(false);
  const [stats, setStats] = useState<PlayerStats | null>(null);
  const StatsError = useCallback(
    () => (
      <div
        role="dialog"
        aria-label={t("player-stats")}
        onClick={(event) => event.stopPropagation()}
        className="bg-background/95 text-foreground absolute top-4 right-4 z-20 max-w-full space-y-3 rounded-lg border p-3 text-sm"
      >
        <h3 className="font-medium">{t("player-stats")}</h3>
        <p role="alert">{t("unexpected-error")}</p>
        <Button
          className="min-h-11"
          variant="ghost"
          onClick={() => setShowStats(false)}
        >
          {t("close")}
        </Button>
      </div>
    ),
    [t],
  );
  // Stable per-mount id for video playback
  const [sessionId] = useSonare();

  // Refs so the video event-listener effect below doesn't have to list
  // these as deps; it would otherwise tear down and re-add its
  // listeners on every transport toggle or parent re-render.
  const useWebRTCRef = useRef(useWebRTC);
  const onErrorRef = useRef(onError);
  useWebRTCRef.current = transport === "webrtc";
  onErrorRef.current = onError;

  const surfaceError = useCallback(
    (msg: string) => {
      if (isStreamer) {
        setError(msg);
        onErrorRef.current?.(msg);
        return;
      }
      // WebRTC failed; fall back to HLS automatically.
      if (useWebRTCRef.current) {
        setUseWebRTC(false);
        writeTransportPreference(false);
      }
    },
    [isStreamer],
  );

  const handleWebRTCChange = useCallback(
    (value: boolean) => {
      const video = videoRef.current;
      if (video) {
        if (transport === "moq") {
          video.muted = moqMuted;
          video.volume = moqVolume;
        } else if (value) {
          setMoqMuted(video.muted);
          setMoqVolume(video.volume);
        }
      }
      setMoqUnavailableFor(null);
      setMoqPaused(false);
      setUseWebRTC(value);
      writeTransportPreference(value);
    },
    [transport, moqMuted, moqVolume],
  );

  const handlePlaying = useCallback(() => {
    setPlaying(true);
    setBuffering(false);
    setHasPlayed(true);
    setManifestReady(true);
    setError(null);
    onPlaying?.();
  }, [onPlaying]);

  const handleMoqUnavailable = useCallback(() => {
    if (videoRef.current) {
      videoRef.current.muted = moqMuted;
      videoRef.current.volume = moqVolume;
    }
    setMoqUnavailableFor(src);
  }, [src, moqMuted, moqVolume]);

  const toggleMoqPlay = useCallback(() => {
    setMoqPaused(playing);
    setPlaying(!playing && hasPlayed);
  }, [playing, hasPlayed]);

  // Reset per-source state when src or active changes.
  useEffect(() => {
    setError(null);
    setPlaying(false);
    setMoqPaused(false);
    setMoqUnavailableFor(null);
    setBuffering(active);
    setHasPlayed(false);
    setManifestReady(false);
    setQualities([]);
    setCurrentQuality(-1);
    setStats(null);
  }, [src, active]);

  // reset chrome's per-transport state.
  useEffect(() => {
    setPlaying(false);
    setBuffering(active);
    setHasPlayed(false);
    setManifestReady(false);
    setQualities([]);
    setCurrentQuality(-1);
    setStats(null);
  }, [active, transport]);

  // Mirror the video element's state into React.
  useEffect(() => {
    if (transport === "moq") return;
    const video = videoRef.current;
    if (!video) return;

    const onPlay = () => setPlaying(true);
    const onPause = () => setPlaying(false);
    const onLoadedMetadata = () => setManifestReady(true);
    const onBufferingChange = (event: Event) => {
      setBuffering(getBufferingState(event.type as BufferingMediaEvent));
    };
    const onErrorEvt = () => {
      const code = video.error?.code;
      // MediaError code 1 (MEDIA_ERR_ABORTED) fires when the user or
      // a backend tears down the source; not a real error to surface.
      if (code === 1) return;
      surfaceError(
        code === 2
          ? t("player-error-network")
          : code === 3
            ? t("player-error-playback")
            : code === 4
              ? t("player-error-format")
              : t("player-error-playback"),
      );
    };

    video.addEventListener("play", onPlay);
    video.addEventListener("pause", onPause);
    video.addEventListener("playing", handlePlaying);
    video.addEventListener("loadedmetadata", onLoadedMetadata);
    video.addEventListener("error", onErrorEvt);
    const bufferingEvents = [
      "loadstart",
      "waiting",
      "seeking",
      "canplay",
      "seeked",
      "pause",
      "ended",
      "error",
    ] satisfies BufferingMediaEvent[];
    for (const event of bufferingEvents) {
      video.addEventListener(event, onBufferingChange);
    }

    return () => {
      video.removeEventListener("play", onPlay);
      video.removeEventListener("pause", onPause);
      video.removeEventListener("playing", handlePlaying);
      video.removeEventListener("loadedmetadata", onLoadedMetadata);
      video.removeEventListener("error", onErrorEvt);
      for (const event of bufferingEvents) {
        video.removeEventListener(event, onBufferingChange);
      }
    };
  }, [active, transport, handlePlaying, surfaceError]);

  // Big play button shows whenever playback is gated (paused, or
  // manifest not yet ready and the user hasn't successfully played).
  const showBigPlay = active && (!playing || (!hasPlayed && manifestReady));
  const showBuffering = shouldShowBufferingIndicator({
    active,
    buffering,
    bigPlay: showBigPlay,
    hasError: !!error,
  });
  const bufferingOverlay = getBufferingOverlayPresentation(showBuffering);

  // Auto-hide controls on idle. Reset on any pointer activity.
  const bumpControls = useCallback(() => {
    setShowControls(true);
    if (idleTimerRef.current) clearTimeout(idleTimerRef.current);
    if (playing && !error) {
      idleTimerRef.current = setTimeout(
        () => setShowControls(false),
        IDLE_HIDE_MS,
      );
    }
  }, [playing, error]);

  useEffect(() => {
    bumpControls();
    return () => {
      if (idleTimerRef.current) clearTimeout(idleTimerRef.current);
    };
  }, [bumpControls]);

  const setQuality = useCallback(
    (index: number) => {
      backendRef.current?.setQuality(index);
      if (transport === "hls") writeQualityPreference(index);
    },
    [transport],
  );

  return (
    <div
      ref={containerRef}
      data-playback-transport={active ? transport : undefined}
      className="group relative h-full w-full bg-black"
      onMouseMove={bumpControls}
      onMouseLeave={() => {
        if (playing && !error) {
          if (idleTimerRef.current) clearTimeout(idleTimerRef.current);
          idleTimerRef.current = setTimeout(
            () => setShowControls(false),
            IDLE_HIDE_MS,
          );
        }
      }}
      onClick={() => {
        if (showBigPlay) return; // big play button handles it
        if (transport === "moq") return toggleMoqPlay();
        const video = videoRef.current;
        if (!video) return;
        if (video.paused) {
          video
            .play()
            .catch((err) => console.warn("[player] play() rejected", err));
        } else {
          video.pause();
        }
      }}
    >
      {active ? (
        <video
          ref={videoRef}
          poster={poster}
          muted
          autoPlay
          playsInline
          className="absolute inset-0 h-full w-full object-contain"
        />
      ) : (
        <img
          src={fallbackPoster ?? poster}
          alt=""
          className="absolute inset-0 h-full w-full bg-black object-contain"
        />
      )}

      {active && transport === "moq" && (
        <canvas
          ref={canvasRef}
          aria-hidden="true"
          className={cn(
            "absolute inset-0 h-full w-full object-contain",
            hasPlayed ? "opacity-100" : "opacity-0",
          )}
        />
      )}

      {active && transport === "moq" ? (
        <MoQPlayer
          ref={backendRef}
          canvasRef={canvasRef}
          src={src}
          paused={moqPaused}
          muted={moqMuted}
          volume={moqVolume}
          onUnavailable={handleMoqUnavailable}
          onPlaying={handlePlaying}
          onBufferingChange={setBuffering}
          onQualitiesChange={setQualities}
          onCurrentQualityChange={setCurrentQuality}
          onStatsChange={setStats}
        />
      ) : (
        active && (
          <PlayerBackend
            src={src}
            useWebRTC={transport === "webrtc"}
            mode={mode}
            startTime={startTime}
            lowLatency={lowLatency}
            videoRef={videoRef}
            active={active}
            onError={surfaceError}
            onQualitiesChange={setQualities}
            onCurrentQualityChange={setCurrentQuality}
            onStatsChange={setStats}
            ref={backendRef}
          />
        )
      )}

      {active && (
        <div
          aria-hidden={bufferingOverlay.ariaHidden}
          className={cn(
            "pointer-events-none absolute inset-0 z-10 flex items-center justify-center transition-opacity duration-200 ease-in-out motion-reduce:duration-150",
            bufferingOverlay.opacityClass,
          )}
        >
          <Loader label={t("player-buffering")} className="text-white" />
        </div>
      )}

      {active && overlay}

      {active && (
        <ContentWarningOverlay
          warnings={contentWarnings}
          portalContainer={fullscreen ? containerRef : undefined}
        />
      )}

      {active && (
        <PlayerControls
          playback={
            transport === "moq"
              ? {
                  playing,
                  muted: moqMuted,
                  volume: moqVolume,
                  togglePlay: toggleMoqPlay,
                  toggleMute: () => setMoqMuted(!moqMuted),
                  setVolume: (value) => {
                    setMoqVolume(value);
                    if (value > 0) setMoqMuted(false);
                  },
                }
              : undefined
          }
          videoRef={videoRef}
          containerRef={containerRef}
          isLive={mode === "live"}
          showControls={showControls}
          bigPlay={showBigPlay}
          forceVisible={!!error}
          qualities={qualities}
          currentQuality={currentQuality}
          onQualityChange={setQuality}
          useWebRTC={useWebRTC}
          onUseWebRTCChange={handleWebRTCChange}
          showStats={showStats}
          onShowStatsChange={setShowStats}
          showDanmu={showDanmu}
          onShowDanmuChange={(v) => onShowDanmuChange?.(v)}
        />
      )}

      {active && showStats && stats && (
        <CatchBoundary
          getResetKey={() => `${src}:${mode}:${transport}`}
          errorComponent={StatsError}
        >
          <Suspense fallback={null}>
            <StatsOverlay
              key={`${src}:${mode}:${transport}`}
              portalContainer={fullscreen ? containerRef : undefined}
              stats={stats}
              live={mode === "live"}
              protocol={
                transport === "moq"
                  ? "MoQ"
                  : transport === "webrtc"
                    ? t("player-protocol-webrtc")
                    : t("player-protocol-hls")
              }
              latencyMode={
                transport === "hls" && !lowLatency
                  ? t("player-latency-standard")
                  : t("player-latency-low")
              }
              renderSurface={transport === "moq" ? "canvas" : "video"}
              sessionId={sessionId}
            />
          </Suspense>
        </CatchBoundary>
      )}

      {error && (
        <div
          className="pointer-events-auto absolute top-2 right-2 left-2 rounded bg-red-500/90 px-3 py-2 text-sm text-white"
          role="alert"
        >
          {error}
        </div>
      )}
    </div>
  );
}

/**
 * Picks the playback backend for the current `src` and renders it. New
 * transports (WebRTC, direct MP4, external embeds, etc.) go here.
 */
function PlayerBackend({
  src,
  useWebRTC,
  mode,
  startTime,
  lowLatency,
  videoRef,
  active,
  onError,
  onQualitiesChange,
  onCurrentQualityChange,
  onStatsChange,
  ref,
}: {
  src: string;
  useWebRTC: boolean;
  mode: "live" | "vod";
  startTime?: number;
  lowLatency: boolean;
  videoRef: RefObject<HTMLVideoElement | null>;
  active: boolean;
  onError: (msg: string) => void;
  onQualitiesChange: (qualities: QualityOption[]) => void;
  onCurrentQualityChange: (index: number) => void;
  onStatsChange: (stats: PlayerStats) => void;
  ref: RefObject<PlayerBackendHandle | null>;
}) {
  if (mode === "live" && (useWebRTC || src.startsWith("webrtc://"))) {
    return (
      <WebRTCPlayer
        ref={ref}
        videoRef={videoRef}
        src={src}
        active={active}
        onError={onError}
        onStatsChange={onStatsChange}
      />
    );
  }
  return (
    <HLSPlayer
      ref={ref}
      videoRef={videoRef}
      src={src}
      active={active}
      mode={mode}
      startTime={startTime}
      lowLatency={lowLatency}
      onError={onError}
      onQualitiesChange={onQualitiesChange}
      onCurrentQualityChange={onCurrentQualityChange}
      onStatsChange={onStatsChange}
    />
  );
}
