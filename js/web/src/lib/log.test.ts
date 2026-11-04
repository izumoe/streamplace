import * as Sentry from "@sentry/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { captureError } from "./log";

const events: Sentry.ErrorEvent[] = [];

beforeEach(() => {
  vi.stubEnv("DEV", false);
  vi.stubEnv("VITE_SENTRY_DSN", "https://public@example.com/1");
  events.length = 0;
  Sentry.init({
    dsn: import.meta.env["VITE_SENTRY_DSN"],
    defaultIntegrations: false,
    transport: () => ({
      send: async (envelope) => {
        for (const [header, payload] of envelope[1]) {
          if (header.type === "event")
            events.push(payload as Sentry.ErrorEvent);
        }
        return { statusCode: 200 };
      },
      flush: async () => true,
    }),
  });
});

afterEach(async () => {
  await Sentry.close();
  vi.unstubAllEnvs();
  vi.restoreAllMocks();
});

it.each([undefined, ""])("does not report when the DSN is %s", async (dsn) => {
  vi.stubEnv("VITE_SENTRY_DSN", dsn);
  captureError("disabled reporting", { stream: "test" });
  await Sentry.flush();
  expect(events).toEqual([]);
});

it("reports the error and context without leaking context into later errors", async () => {
  captureError("first error", { stream: "test" });
  captureError("second error");
  await Sentry.flush();
  expect(events).toHaveLength(2);
  expect(events[0].exception?.values?.[0]).toMatchObject({
    type: "Error",
    value: "first error",
  });
  expect(events[0].extra).toMatchObject({ stream: "test" });
  expect(events[1].exception?.values?.[0]?.value).toBe("second error");
  expect(events[1].extra?.stream).toBeUndefined();
});

it("logs development errors without reporting them", async () => {
  vi.stubEnv("DEV", true);
  const consoleError = vi.spyOn(console, "error").mockImplementation(() => {});
  captureError("development error", { stream: "test" });
  await Sentry.flush();
  expect(consoleError).toHaveBeenCalledExactlyOnceWith("development error", {
    stream: "test",
  });
  expect(events).toEqual([]);
});
