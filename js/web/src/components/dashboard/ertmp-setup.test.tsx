import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { toast } from "sonner";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { ERTMPSetup } from "./ertmp-setup";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
  Trans: ({ i18nKey }: { i18nKey: string }) => i18nKey,
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

let root: Root;
let container: HTMLDivElement;
let copied: string;
let writeText: (text: string) => Promise<void>;

beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  copied = "";
  writeText = async (text) => {
    copied = text;
  };
  vi.stubGlobal("navigator", {
    userAgent: navigator.userAgent,
    clipboard: { writeText: (text: string) => writeText(text) },
  });
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
});

afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
  vi.unstubAllGlobals();
  vi.clearAllMocks();
});

async function render() {
  await act(async () => root.render(<ERTMPSetup />));
}

async function config() {
  await act(async () => copyButton().click());
  return JSON.parse(copied);
}

function copyButton() {
  return Array.from(container.querySelectorAll("button")).find((button) =>
    button.textContent?.includes("ertmp-copy"),
  )!;
}

async function openSelect(suffix: string) {
  const trigger = container.querySelector<HTMLButtonElement>(
    `[role="combobox"][id$="-${suffix}"]`,
  )!;
  expect(trigger).not.toBeNull();
  await act(async () => trigger.click());
}

function option(label: string) {
  const element = Array.from(
    document.querySelectorAll<HTMLElement>('[role="option"]'),
  ).find((item) => item.textContent === label);
  expect(element).toBeDefined();
  return element!;
}

async function selectOption(suffix: string, label: string) {
  await openSelect(suffix);
  await act(async () => option(label).click());
}

async function closeSelect() {
  await act(async () => {
    document.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Escape", bubbles: true }),
    );
  });
}

it("generates an aligned three-rung x264 ladder and one AAC track without connection credentials", async () => {
  await render();
  const value = await config();
  expect(Object.keys(value).sort()).toEqual([
    "audio_configurations",
    "encoder_configurations",
  ]);
  expect(
    value.encoder_configurations.map(
      (encoder: {
        width: number;
        height: number;
        settings: { bitrate: number };
      }) => [encoder.width, encoder.height, encoder.settings.bitrate],
    ),
  ).toEqual([
    [1920, 1080, 5000],
    [1280, 720, 3250],
    [640, 360, 1000],
  ]);
  for (const encoder of value.encoder_configurations) {
    expect(encoder).toMatchObject({
      type: "obs_x264",
      framerate: { numerator: 30, denominator: 1 },
      canvas_index: 0,
      settings: {
        rate_control: "CBR",
        keyint_sec: 1,
        profile: "high",
        preset: "veryfast",
        tune: "zerolatency",
      },
    });
  }
  expect(value.audio_configurations.live).toEqual([
    {
      codec: "ffmpeg_aac",
      track_id: 1,
      channels: 2,
      settings: { bitrate: 128 },
    },
  ]);
});

it("changes every rendition to Apple hardware settings without retaining x264 options", async () => {
  await render();
  await selectOption("encoder", "ertmp-encoder-apple");
  for (const encoder of (await config()).encoder_configurations) {
    expect(encoder.type).toBe("com.apple.videotoolbox.videoencoder.ave.avc");
    expect(encoder.settings.bframes).toBe(false);
    expect(encoder.settings).not.toHaveProperty("preset");
    expect(encoder.settings).not.toHaveProperty("tune");
  }
});

it("confirms copying only after the clipboard accepts the configuration", async () => {
  let accept: () => void = () => {};
  writeText = (text) =>
    new Promise<void>((resolve) => {
      accept = () => {
        copied = text;
        resolve();
      };
    });
  await render();
  await act(async () => copyButton().click());
  expect(toast.success).not.toHaveBeenCalled();
  await act(async () => accept());
  expect(JSON.parse(copied).encoder_configurations).toHaveLength(3);
  expect(toast.success).toHaveBeenCalledWith("copied-to-clipboard");
});

it("reports a clipboard denial without a false success confirmation", async () => {
  writeText = async () => {
    throw new Error("clipboard denied");
  };
  await render();
  await act(async () => copyButton().click());
  expect(copied).toBe("");
  expect(toast.error).toHaveBeenCalledWith("share-copy-failed");
  expect(toast.success).not.toHaveBeenCalled();
});

it("keeps a fixed H.264 ladder beneath 1440p AV1 on capable encoders", async () => {
  await render();
  await openSelect("resolution");
  expect(option("ertmp-1440-unavailable").getAttribute("aria-disabled")).toBe(
    "true",
  );
  await closeSelect();
  await selectOption("encoder", "ertmp-encoder-nvidia");
  await selectOption("resolution", "1440p · AV1");
  expect(
    (await config()).encoder_configurations.map(
      (quality: { type: string }) => quality.type,
    ),
  ).toEqual([
    "obs_nvenc_av1_tex",
    "obs_nvenc_h264_tex",
    "obs_nvenc_h264_tex",
    "obs_nvenc_h264_tex",
  ]);
  await openSelect("encoder");
  expect(option("ertmp-encoder-apple").getAttribute("aria-disabled")).toBe(
    "true",
  );
  await closeSelect();
  expect(container.querySelectorAll('input[type="checkbox"]')).toHaveLength(0);
  expect(
    (await config()).encoder_configurations.map(
      (quality: { height: number }) => quality.height,
    ),
  ).toEqual([1440, 1080, 720, 360]);
});

it.each([
  ["30", [9000, 5000, 3250, 1000], "20.2 - 26 Mbps"],
  ["60", [10450, 6000, 3750, 1000], "23.5 - 30 Mbps"],
] as const)(
  "uses midpoint bases and the %s fps budget for the config and upload estimate",
  async (fps, expected, upload) => {
    await render();
    await selectOption("encoder", "ertmp-encoder-nvidia");
    await selectOption("resolution", "1440p · AV1");
    await selectOption("fps", `${fps} fps`);
    expect(
      (await config()).encoder_configurations.map(
        (quality: { settings: { bitrate: number } }) =>
          quality.settings.bitrate,
      ),
    ).toEqual(expected);
    expect(container.querySelector("output")!.textContent).toBe(upload);
    expect(
      container.querySelector("tbody tr:nth-child(4) td")!.textContent,
    ).toBe("1000");
  },
);

it("keeps all bitrates read-only and OBS setup collapsed beneath the preset", async () => {
  await render();
  const setup = container.querySelector<HTMLDetailsElement>(
    'details[id$="-obs"]',
  )!;
  expect(setup).not.toBeNull();
  expect(setup.open).toBe(false);
  const steps = setup.querySelector("ol")!.children;
  expect(steps).toHaveLength(3);
  expect(setup.contains(container.querySelector("button"))).toBe(false);
  expect(setup.contains(container.querySelector("table"))).toBe(false);
  expect(container.querySelectorAll("table input, table select")).toHaveLength(
    0,
  );
  expect(
    container.querySelectorAll(
      'input:not([type="hidden"]):not([aria-hidden="true"])',
    ),
  ).toHaveLength(0);
  expect(container.querySelectorAll('[role="combobox"]')).toHaveLength(3);
  expect(container.querySelector('details[id$="-customize"]')).toBeNull();
  expect(steps[2].textContent).toContain("1920×1080 · 30 fps · 48 kHz");
  expect(container.querySelector("output")!.textContent).toBe("10.3 - 13 Mbps");
  for (const id of container
    .querySelector("output")!
    .getAttribute("aria-describedby")!
    .split(" ")) {
    expect(document.getElementById(id)).not.toBeNull();
  }
});

it("shows AV1 help only for 1440p and keeps the OBS canvas and fps instructions current", async () => {
  await render();
  expect(container.querySelector('p[id$="-encoder-help"]')).toBeNull();
  expect(container.querySelector('p[id$="-av1-help"]')).toBeNull();
  await openSelect("resolution");
  expect(option("ertmp-1440-unavailable").getAttribute("aria-disabled")).toBe(
    "true",
  );
  await closeSelect();
  await selectOption("encoder", "ertmp-encoder-nvidia");
  expect(container.querySelector('p[id$="-av1-help"]')).toBeNull();
  await selectOption("resolution", "1440p · AV1");
  await selectOption("fps", "60 fps");
  expect(container.querySelector('p[id$="-av1-help"]')).not.toBeNull();
  expect(container.querySelector("ol")!.children[2].textContent).toContain(
    "2560×1440 · 60 fps · 48 kHz",
  );
  expect(container.querySelector('[role="note"]')!.textContent).toContain(
    "ertmp-high-upload",
  );
});
