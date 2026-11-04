import { loginThroughPds } from "../../e2e-web/flows/login";
import { expect, test } from "./fixtures";

test.use({ trace: "off", video: "off" });

test("stream settings provides fixed OBS presets with secondary setup guidance on desktop and mobile", async ({
  page,
  context,
}, testInfo) => {
  const server = process.env.SERVER_HTTPS_URL;
  test.skip(
    !server || !process.env.PDS_HTTPS_URL,
    "requires the real TLS PDS harness",
  );
  await loginThroughPds(page);
  await page.goto(`${server}/dashboard/stream`);
  await page
    .getByRole("link", { name: "Expanded Broadcasting", exact: true })
    .click();
  const section = page.locator("#ertmp");
  await expect(
    section.getByRole("heading", {
      name: "Expanded Broadcasting",
      exact: true,
    }),
  ).toBeVisible();
  await expect(
    section.getByText("Requires OBS 32 or newer", { exact: false }),
  ).toBeVisible();
  const setup = section.locator('details[id$="-obs"]');
  const steps = setup.locator("ol > li");
  await expect(setup).toHaveJSProperty("open", false);
  await expect(steps).toHaveCount(3);
  await expect(section.getByRole("table").locator("input, select")).toHaveCount(
    0,
  );
  await expect(section.getByRole("spinbutton")).toHaveCount(0);
  await expect(section.getByRole("combobox")).toHaveCount(3);
  await expect(section.locator("output")).toHaveText("10.3 - 13 Mbps");
  await expect(
    section.getByText("1440p uses AV1", { exact: false }),
  ).toHaveCount(0);
  await setup.locator("summary").click();
  await expect(steps.nth(2)).toContainText("1920×1080 · 30 fps · 48 kHz");
  await expect(steps.nth(2)).toBeVisible();
  await setup.locator("summary").click();
  await expect(
    page.locator('[data-slot="select-content"]:visible'),
  ).toHaveCount(0);
  await section.screenshot({
    path: testInfo.outputPath("ertmp-settings-desktop-default.png"),
  });

  const encoder = section.getByRole("combobox", {
    name: "Encoder",
    exact: true,
  });
  const resolution = section.getByRole("combobox", {
    name: "Top quality",
    exact: true,
  });
  await resolution.click();
  const unavailable = page.getByRole("option", {
    name: "1440p · AV1 (needs an AV1 encoder)",
    exact: true,
  });
  await expect(unavailable).toBeDisabled();
  await page.keyboard.press("Escape");
  await encoder.click();
  await page.getByRole("option", { name: "NVIDIA NVENC", exact: true }).click();
  await resolution.click();
  await page.getByRole("option", { name: "1440p · AV1", exact: true }).click();
  await encoder.click();
  await expect(
    page.getByRole("option", { name: "Apple VideoToolbox", exact: true }),
  ).toBeDisabled();
  await page.keyboard.press("Escape");
  await section
    .getByRole("combobox", { name: "Frame rate", exact: true })
    .click();
  await page.getByRole("option", { name: "60 fps", exact: true }).click();
  await expect(
    section.getByText("1440p uses AV1", { exact: false }),
  ).toBeVisible();
  await expect(section.locator("output")).toHaveText("23.5 - 30 Mbps");
  await expect(section.getByRole("note")).toBeVisible();
  await setup.locator("summary").click();
  await expect(steps.nth(2)).toContainText("2560×1440 · 60 fps · 48 kHz");
  await setup.locator("summary").click();
  await expect(
    page.locator('[data-slot="select-content"]:visible'),
  ).toHaveCount(0);
  await section.screenshot({
    path: testInfo.outputPath("ertmp-settings-desktop.png"),
  });
  await context.grantPermissions(["clipboard-read", "clipboard-write"], {
    origin: new URL(server!).origin,
  });
  await section
    .getByRole("button", { name: "Copy OBS configuration", exact: true })
    .click();
  const config = JSON.parse(
    await page.evaluate(() => navigator.clipboard.readText()),
  );
  expect(
    config.encoder_configurations.map(
      (quality: {
        type: string;
        height: number;
        settings: { bitrate: number };
      }) => [quality.type, quality.height, quality.settings.bitrate],
    ),
  ).toEqual([
    ["obs_nvenc_av1_tex", 1440, 10450],
    ["obs_nvenc_h264_tex", 1080, 6000],
    ["obs_nvenc_h264_tex", 720, 3750],
    ["obs_nvenc_h264_tex", 360, 1000],
  ]);
  expect(config.audio_configurations.live[0].settings.bitrate).toBe(128);
  await resolution.click();
  await page
    .getByRole("option", { name: "1080p · H.264", exact: true })
    .click();
  await expect(section.getByRole("note")).toHaveCount(0);
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(
    page.locator('[data-slot="select-content"]:visible'),
  ).toHaveCount(0);
  await section.screenshot({
    path: testInfo.outputPath("ertmp-settings-mobile.png"),
  });
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(390);
  await encoder.click();
  const listbox = page.getByRole("listbox");
  await expect(listbox).toBeVisible();
  await expect(page.locator('[data-slot="select-content"]:visible')).toHaveCSS(
    "opacity",
    "1",
  );
  const popup = (await listbox.boundingBox())!;
  expect(popup.x).toBeGreaterThanOrEqual(0);
  expect(popup.x + popup.width).toBeLessThanOrEqual(390);
  await page.screenshot({
    path: testInfo.outputPath("ertmp-settings-mobile-dropdown.png"),
  });
  await page.keyboard.press("Escape");
  const bounds = (await encoder.boundingBox())!;
  expect(bounds.height).toBeGreaterThanOrEqual(44);
  expect(bounds.width).toBeLessThan(390);
  await section
    .getByRole("button", { name: "Copy OBS configuration", exact: true })
    .click();
  const mobileConfig = JSON.parse(
    await page.evaluate(() => navigator.clipboard.readText()),
  );
  expect(
    mobileConfig.encoder_configurations.map(
      (quality: { height: number }) => quality.height,
    ),
  ).toEqual([1080, 720, 360]);
});
