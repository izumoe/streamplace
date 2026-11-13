import { afterEach, expect, it, vi } from "vitest";
import { createMoqSession } from "./moq-session";

afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

it("counts only progress, reuses the signed lease and closes it on fallback", async () => {
  vi.useFakeTimers();
  const requests: { url: URL; body: { sid: string; closed: boolean } }[] = [];
  vi.stubGlobal("fetch", async (url: URL, init: RequestInit) => {
    requests.push({ url, body: JSON.parse(init.body as string) });
    return Response.json({ sid: "signed-session" });
  });
  const session = createMoqSession(
    new URL("https://origin.example/api/moq/session?streamer=alice"),
    "owner-session",
  );
  expect(requests).toHaveLength(0);
  await session.touch();
  expect(requests[0].url.pathname).toBe("/api/moq/session");
  expect(requests[0].body.sid).toBe("owner-session");
  await session.touch();
  expect(requests).toHaveLength(1);
  await vi.advanceTimersByTimeAsync(10_000);
  await session.touch();
  expect(requests[1].body.sid).toBe("signed-session");
  session.close();
  await Promise.resolve();
  expect(requests[2].body).toEqual({ sid: "signed-session", closed: true });
  await session.touch();
  expect(requests).toHaveLength(3);
});

it("cleans up a first lease whose response arrives after unmount", async () => {
  let respond!: (response: Response) => void;
  const bodies: unknown[] = [];
  vi.stubGlobal("fetch", async (_url: URL, init: RequestInit) => {
    bodies.push(JSON.parse(init.body as string));
    if (bodies.length === 1)
      return new Promise<Response>((resolve) => {
        respond = resolve;
      });
    return new Response(null, { status: 204 });
  });
  const session = createMoqSession(
    new URL("https://origin.example/api/moq/session?streamer=alice"),
  );
  const pending = session.touch();
  session.close();
  respond(Response.json({ sid: "late-session" }));
  await pending;
  expect(bodies).toEqual([
    { sid: "", closed: false },
    { sid: "late-session", closed: true },
  ]);
});

it("retries a failed heartbeat without affecting playback", async () => {
  vi.useFakeTimers();
  const fetch = vi
    .fn()
    .mockRejectedValueOnce(new Error("offline"))
    .mockResolvedValue(Response.json({ sid: "ok" }));
  vi.stubGlobal("fetch", fetch);
  const session = createMoqSession(
    new URL("https://origin.example/api/moq/session?streamer=alice"),
  );
  await expect(session.touch()).resolves.toBeUndefined();
  await vi.advanceTimersByTimeAsync(10_000);
  await session.touch();
  expect(fetch).toHaveBeenCalledTimes(2);
  session.close();
});

it("keeps synchronous reporting failures out of player teardown", async () => {
  vi.stubGlobal("fetch", async () => Response.json({ sid: "session" }));
  const session = createMoqSession(
    new URL("https://origin.example/api/moq/session?streamer=alice"),
  );
  await session.touch();
  vi.stubGlobal("fetch", () => {
    throw new Error("fetch unavailable during teardown");
  });
  expect(() => session.close()).not.toThrow();
});
