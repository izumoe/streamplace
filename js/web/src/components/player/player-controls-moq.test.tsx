import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { FullscreenProvider } from "../../contexts/fullscreen-context";
import { PlayerControls } from "./player-controls";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

function CanvasControls() {
  const [playing, setPlaying] = useState(true);
  const [muted, setMuted] = useState(true);
  const [volume, setVolume] = useState(1);
  return (
    <FullscreenProvider>
      <PlayerControls
        videoRef={{ current: null }}
        containerRef={{ current: null }}
        isLive
        showControls
        bigPlay={false}
        qualities={[]}
        currentQuality={-1}
        onQualityChange={() => {}}
        useWebRTC
        onUseWebRTCChange={() => {}}
        showStats={false}
        onShowStatsChange={() => {}}
        showDanmu={false}
        onShowDanmuChange={() => {}}
        playback={{
          playing,
          muted,
          volume,
          togglePlay: () => setPlaying((value) => !value),
          toggleMute: () => setMuted((value) => !value),
          setVolume,
        }}
      />
    </FullscreenProvider>
  );
}

describe("canvas playback controls", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(async () => {
    vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    await act(async () => root.render(<CanvasControls />));
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
    vi.unstubAllGlobals();
  });

  it("pauses and resumes the canvas without an HTML video element", async () => {
    const pause = container.querySelector<HTMLButtonElement>(
      'button[title="player-pause"]',
    );
    expect(pause).not.toBeNull();
    await act(async () => pause?.click());
    const play = container.querySelector<HTMLButtonElement>(
      'button[title="player-play"]',
    );
    expect(play).not.toBeNull();
    await act(async () => play?.click());
    expect(
      container.querySelector('button[title="player-pause"]'),
    ).not.toBeNull();
  });

  it("unmutes canvas playback from the autoplay prompt and keyboard", async () => {
    const prompt = container.querySelector<HTMLButtonElement>(
      'button[aria-label="player-unmute"][aria-hidden="false"]',
    );
    expect(prompt).not.toBeNull();
    await act(async () => prompt?.click());
    expect(
      container.querySelector('button[title="player-mute"]'),
    ).not.toBeNull();
    await act(async () =>
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "m" })),
    );
    expect(
      container.querySelector('button[title="player-unmute"]'),
    ).not.toBeNull();
  });
});
