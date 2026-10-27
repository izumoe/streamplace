import type { AppBskyActorDefs } from "@atproto/api";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { useStore } from "../lib/store";
import useAvatars from "./use-avatars";

const alice = "did:plc:abcdefghijklmnopqrstuvwx";
const bob = "did:plc:zyxwvutsrqponmlkjihgfedc";
let profiles: ReturnType<typeof useAvatars>;
let root: ReturnType<typeof createRoot>;
let pending: ((response: Response) => void)[];
let requests: string[][];
let errorLog: ReturnType<typeof vi.spyOn>;

function Profiles({ dids }: { dids: string[] }) {
  profiles = useAvatars(dids);
  return null;
}

async function render(dids: string[]) {
  await act(async () => root.render(<Profiles dids={dids} />));
}

async function respond(profileData: AppBskyActorDefs.ProfileViewDetailed[]) {
  const previous = useStore.getState().profileCache;
  const resolve = pending.shift();
  expect(resolve).toBeDefined();
  await act(async () => {
    resolve!(Response.json({ profiles: profileData }));
    await vi.waitFor(() => {
      expect(useStore.getState().profileCache).not.toBe(previous);
    });
  });
}

beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  pending = [];
  requests = [];
  useStore.setState({ profileCache: {} });
  errorLog = vi.spyOn(console, "error").mockImplementation(() => {});
  vi.stubGlobal(
    "fetch",
    vi.fn<typeof fetch>((input) => {
      const url = new URL(
        typeof input === "string"
          ? input
          : input instanceof URL
            ? input.href
            : input.url,
      );
      expect(url.origin).toBe("https://public.api.bsky.app");
      expect(url.pathname).toBe("/xrpc/app.bsky.actor.getProfiles");
      requests.push(url.searchParams.getAll("actors"));
      return new Promise<Response>((resolve) => pending.push(resolve));
    }),
  );
  root = createRoot(document.createElement("div"));
});

afterEach(async () => {
  await act(async () => root.unmount());
  await act(async () => {
    for (const resolve of pending.splice(0))
      resolve(Response.json({ profiles: [] }));
  });
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

it("does not refetch an omitted profile after cache updates and equivalent rerenders", async () => {
  await render([alice]);
  expect(requests).toEqual([[alice]]);

  await respond([]);
  await render([alice]);
  await act(async () => {
    useStore.setState({
      profileCache: { ...useStore.getState().profileCache },
    });
  });

  expect(requests).toEqual([[alice]]);
  expect(profiles).toEqual({});
  expect(errorLog).not.toHaveBeenCalled();
});

it("fetches new DIDs and can retry an omitted DID when the requested list changes", async () => {
  await render([alice]);
  await respond([]);

  await render([bob]);
  expect(requests).toEqual([[alice], [bob]]);
  await respond([{ did: bob, handle: "bob.example" }]);

  await render([alice]);
  expect(requests).toEqual([[alice], [bob], [alice]]);
  await respond([{ did: alice, handle: "alice.example" }]);

  expect(profiles[alice]?.handle).toBe("alice.example");
  expect(profiles[bob]?.handle).toBe("bob.example");
  expect(errorLog).not.toHaveBeenCalled();
});

it("does not loop on a transient failure and retries after remount", async () => {
  await render([alice]);
  const resolve = pending.shift();
  expect(resolve).toBeDefined();
  await act(async () => {
    resolve!(
      Response.json(
        { error: "InternalServerError", message: "AppView unavailable" },
        { status: 503 },
      ),
    );
    await vi.waitFor(() => expect(errorLog).toHaveBeenCalled());
  });
  await render([alice]);
  expect(requests).toEqual([[alice]]);
  expect(profiles).toEqual({});
  expect(errorLog).toHaveBeenCalledWith("getProfiles error", expect.any(Error));

  await act(async () => root.unmount());
  root = createRoot(document.createElement("div"));
  await render([alice]);
  expect(requests).toEqual([[alice], [alice]]);
  await respond([{ did: alice, handle: "alice.example" }]);
  expect(profiles[alice]?.handle).toBe("alice.example");
});
