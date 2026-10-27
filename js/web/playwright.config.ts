import { defineConfig, devices } from "@playwright/test";

// Playwright e2e config for the Streamplace web app.
//
// hack/e2e-web-local.sh --config ../web/playwright.config.ts runs these tests
// against the repository's isolated node/PDS harness. For a running dev server,
// point STREAMPLACE_E2E_BASE_URL at it (Vite defaults to :5173).
//
// Run: pnpm e2e
// Run with UI: pnpm e2e:ui

const BASE_URL =
  process.env.STREAMPLACE_E2E_BASE_URL ??
  process.env.SERVER_URL ??
  "http://127.0.0.1:5173";
const E2E_PROXY_URL = process.env.E2E_PROXY_URL;
const E2E_TLS_SPKI = process.env.E2E_TLS_SPKI;

export default defineConfig({
  testDir: "./e2e",
  fullyParallel: false,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 2 : 0,
  workers: 1,
  reporter: process.env.CI
    ? [["list"], ["html", { open: "never" }]]
    : [["list"]],
  timeout: 90_000,
  expect: { timeout: 30_000 },
  use: {
    baseURL: BASE_URL,
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
    video: "retain-on-failure",
    ...(E2E_PROXY_URL
      ? { proxy: { server: E2E_PROXY_URL, bypass: "127.0.0.1,localhost" } }
      : {}),
    launchOptions: {
      args: [
        "--autoplay-policy=no-user-gesture-required",
        "--use-fake-ui-for-media-stream",
        ...(E2E_TLS_SPKI
          ? [`--ignore-certificate-errors-spki-list=${E2E_TLS_SPKI}`]
          : []),
      ],
    },
  },
  projects: [
    {
      name: "chromium",
      use: { ...devices["Desktop Chrome"] },
    },
  ],
});
