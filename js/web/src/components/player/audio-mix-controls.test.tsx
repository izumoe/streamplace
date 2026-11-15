import { act, useState } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";
import { AudioMixControls } from "./audio-mix-controls";
import type { AudioMix } from "./audio-stems";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

it("adjusts the named group and restores the full mix", async () => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  const host = document.createElement("div");
  document.body.appendChild(host);
  const root = createRoot(host);
  const changes: AudioMix[] = [];
  const shortcut = vi.fn();
  window.addEventListener("keydown", shortcut);
  function Controls() {
    const [mix, setMix] = useState<AudioMix>({ 3: 0.25 });
    return (
      <AudioMixControls
        stems={[
          { alternateGroup: 3, label: "Music" },
          { alternateGroup: 7, label: "Voice" },
        ]}
        mix={mix}
        onMixChange={(next) => {
          changes.push(next);
          setMix(next);
        }}
      />
    );
  }
  try {
    await act(async () => root.render(<Controls />));
    expect(
      host.querySelectorAll<HTMLInputElement>('input[type="range"]')[0]?.value,
    ).toBe("25");
    expect(
      host.querySelectorAll<HTMLInputElement>('input[type="range"]')[1]?.value,
    ).toBe("100");
    const music = host.querySelector<HTMLInputElement>('input[type="range"]')!;
    expect(
      document.getElementById(music.getAttribute("aria-labelledby")!)
        ?.textContent,
    ).toBe("Music");
    await act(async () =>
      music.dispatchEvent(
        new KeyboardEvent("keydown", { key: "ArrowRight", bubbles: true }),
      ),
    );
    expect(changes).toEqual([{ 3: 0.26 }]);
    expect(shortcut).not.toHaveBeenCalled();
    await act(async () =>
      host
        .querySelector("button")!
        .dispatchEvent(
          new KeyboardEvent("keydown", { key: " ", bubbles: true }),
        ),
    );
    expect(shortcut).not.toHaveBeenCalled();
    await act(async () =>
      host.querySelector<HTMLButtonElement>("button")!.click(),
    );
    expect(changes).toEqual([{ 3: 0.26 }, null]);
    expect(host.querySelector("button")?.getAttribute("aria-pressed")).toBe(
      "true",
    );
  } finally {
    window.removeEventListener("keydown", shortcut);
    await act(async () => root.unmount());
    host.remove();
    vi.unstubAllGlobals();
  }
});
