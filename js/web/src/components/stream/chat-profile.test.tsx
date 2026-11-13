import { makeLivestreamStore } from "@streamplace/core";
import { act } from "react";
import { createRoot } from "react-dom/client";
import type { ChatMessageViewHydrated } from "streamplace";
import { afterEach, beforeAll, beforeEach, expect, it, vi } from "vitest";
import { useStore } from "../../lib/store";
import { ChatPanel } from "./chat-panel";

const { warnings } = vi.hoisted(() => ({
  warnings: vi.spyOn(console, "warn").mockImplementation(() => {}),
}));
beforeAll(() => {
  try {
    for (const [message] of warnings.mock.calls)
      expect(String(message)).toMatch(
        /ponyfill implementation|Locks API not available/,
      );
  } finally {
    warnings.mockRestore();
  }
});

vi.mock("../../lib/session", () => ({
  useSession: () => ({
    state: { status: "unauthenticated" },
    did: null,
    pdsAgent: null,
  }),
}));
vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

const did = "did:plc:abcdefghijklmnopqrstuvwx";
const detailed = {
  did,
  handle: "alice.example",
  displayName: "Alice",
  avatar: "https://cdn.bsky.app/alice-avatar.jpg",
  banner: "https://cdn.bsky.app/alice-banner.jpg",
  description: "hello from Bluesky\n<script>plain text</script>",
  pronouns: "she/they",
};
let root: ReturnType<typeof createRoot>;
let container: HTMLDivElement;
let requests: string[][];
let pending: ((response: Response) => void)[];
let errorLog: ReturnType<typeof vi.spyOn>;

beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  useStore.setState({ profileCache: {} });
  requests = [];
  pending = [];
  errorLog = vi.spyOn(console, "error").mockImplementation(() => {});
  vi.stubGlobal(
    "fetch",
    vi.fn<typeof fetch>((input) => {
      const url = new URL(
        input instanceof Request ? input.url : input.toString(),
      );
      expect(url.origin).toBe("https://public.api.bsky.app");
      expect(url.pathname).toBe("/xrpc/app.bsky.actor.getProfiles");
      requests.push(url.searchParams.getAll("actors"));
      return new Promise<Response>((resolve) => pending.push(resolve));
    }),
  );
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
});

afterEach(async () => {
  await act(async () => root.unmount());
  await act(async () => {
    for (const resolve of pending.splice(0))
      resolve(Response.json({ profiles: [] }));
  });
  container.remove();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

async function render(
  author: ChatMessageViewHydrated["author"] = { did, handle: "old.example" },
) {
  const store = makeLivestreamStore();
  const message: ChatMessageViewHydrated = {
    uri: `at://${did}/place.stream.chat.message/one`,
    cid: "bafyreih2o5zat7u4foe4slvdkd3wmhdtd2mvbc66m7lnk7ihw3yxhu3iqe",
    author,
    record: {
      $type: "place.stream.chat.message",
      text: "a chat message",
      streamer: did,
      createdAt:
        new Date().toISOString() as ChatMessageViewHydrated["record"]["createdAt"],
    },
    indexedAt: new Date().toISOString() as ChatMessageViewHydrated["indexedAt"],
  };
  store.setState({ chat: [message] });
  await act(async () => root.render(<ChatPanel store={store} />));
}

async function open() {
  await act(async () =>
    container.querySelector<HTMLElement>("[aria-haspopup]")!.click(),
  );
}

async function expectCard(text: string) {
  await vi.waitFor(async () => {
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    expect(document.querySelector('[role="dialog"]')?.textContent).toContain(
      text,
    );
  });
}

it("loads the full profile only when opened, including banner, bio, and pronouns", async () => {
  await render();
  expect(requests).toEqual([]);
  await open();
  expect(requests).toEqual([[did]]);
  await expectCard("@old.example");
  expect(document.querySelector('[role="dialog"] img')).toBeNull();
  await act(async () =>
    pending.shift()!(Response.json({ profiles: [detailed] })),
  );
  await expectCard("Alice");
  await expectCard("@alice.example");
  await expectCard("she/they");
  await expectCard(detailed.description);
  const card = document.querySelector('[role="dialog"]')!;
  expect(card.querySelector(`img[src="${detailed.avatar}"]`)).not.toBeNull();
  expect(card.querySelector(`img[src="${detailed.banner}"]`)).not.toBeNull();
  expect(card.querySelector("script")).toBeNull();
  expect(errorLog).not.toHaveBeenCalled();
});

it("uses cached profiles without another request and respects cleared profile fields", async () => {
  useStore.setState({
    profileCache: {
      [did]: { did, handle: "alice.example" },
    },
  });
  await render({
    did,
    handle: "old.example",
    displayName: "Old Alice",
    pronouns: "she/her",
    avatar: "https://cdn.bsky.app/old-avatar.jpg",
  });
  await open();
  await expectCard("@alice.example");
  expect(document.querySelector('[role="dialog"]')?.textContent).not.toContain(
    "Old Alice",
  );
  expect(document.querySelector('[role="dialog"]')?.textContent).not.toContain(
    "she/her",
  );
  expect(requests).toEqual([]);
  expect(document.querySelector('[role="dialog"] img')).toBeNull();
  expect(errorLog).not.toHaveBeenCalled();
});

it("keeps the chat identity usable when Bluesky cannot return a profile", async () => {
  await render();
  await open();
  expect(requests).toEqual([[did]]);
  await act(async () => pending.shift()!(Response.json({ profiles: [] })));
  await expectCard("@old.example");
  expect(
    document.querySelector('[role="dialog"] a')?.getAttribute("href"),
  ).toContain(did);
  expect(document.querySelector('[role="dialog"] img')).toBeNull();
  expect(errorLog).not.toHaveBeenCalled();
});

it("keeps the chat identity after a failed profile request without retrying on rerender", async () => {
  await render();
  await open();
  await act(async () =>
    pending.shift()!(
      Response.json(
        { error: "InternalServerError", message: "AppView unavailable" },
        { status: 503 },
      ),
    ),
  );
  await expectCard("@old.example");
  await vi.waitFor(() =>
    expect(errorLog).toHaveBeenCalledWith(
      "getProfiles error",
      expect.any(Error),
    ),
  );
  expect(errorLog).toHaveBeenCalledTimes(1);
  await render();
  await expectCard("@old.example");
  expect(requests).toEqual([[did]]);
});

it("recovers from a broken image when the cached profile supplies a new URL", async () => {
  useStore.setState({ profileCache: { [did]: detailed } });
  await render();
  await open();
  await expectCard("Alice");
  const avatar = document.querySelector<HTMLImageElement>(
    `img[src="${detailed.avatar}"]`,
  )!;
  await act(async () => {
    avatar.dispatchEvent(new Event("error"));
  });
  expect(avatar.style.display).toBe("none");
  const nextAvatar = "https://cdn.bsky.app/new-avatar.jpg";
  await act(async () =>
    useStore.setState({
      profileCache: { [did]: { ...detailed, avatar: nextAvatar } },
    }),
  );
  const recovered = document.querySelector<HTMLImageElement>(
    `img[src="${nextAvatar}"]`,
  )!;
  expect(recovered).not.toBeNull();
  expect(recovered.style.display).not.toBe("none");
  expect(requests).toEqual([]);
  expect(errorLog).not.toHaveBeenCalled();
});
