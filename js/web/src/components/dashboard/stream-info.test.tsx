import { BlobRef } from "@atproto/api";
import type { OAuthSession } from "@atproto/oauth-client-browser";
import { makeLivestreamStore } from "@streamplace/core";
import { createServer, type Server } from "node:http";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { toast } from "sonner";
import { StreamplaceAgent } from "streamplace";
import { afterEach, beforeAll, beforeEach, expect, it, vi } from "vitest";
import { useStore } from "../../lib/store";
import { DashboardStoreContext } from "./dashboard-store-context";
import { StreamInfoWidget } from "./stream-info";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (key: string, options?: { defaultValue?: string }) =>
      options?.defaultValue ?? key,
  }),
}));

const { importWarnings } = vi.hoisted(() => ({
  importWarnings: vi.spyOn(console, "warn").mockImplementation(() => {}),
}));

beforeAll(() => {
  try {
    const allowed = [
      [
        "[@atproto/lex-data]: Uint8Array.fromBase64 / Uint8Array.prototype.toBase64 not available in this environment. Falling back to ponyfill implementation.",
      ],
      [
        "Locks API not available. You should consider using a more recent browser.",
      ],
    ];
    for (const warning of importWarnings.mock.calls)
      expect(allowed).toContainEqual(warning);
  } finally {
    importWarnings.mockRestore();
  }
});

const did = "did:plc:abcdefghijklmnopqrstuvwx";
const cid = "bafyreih2o5zat7u4foe4slvdkd3wmhdtd2mvbc66m7lnk7ihw3yxhu3iqe";
const savedCid = "bafyreif2o5zat7u4foe4slvdkd3wmhdtd2mvbc66m7lnk7ihw3yxhu3iqe";
const uri = `at://${did}/place.stream.livestream/one`;
const initialState = useStore.getState();
let server: Server;
let root: Root;
let container: HTMLDivElement;
let store: ReturnType<typeof makeLivestreamStore>;
let saved: Record<string, any>[];
let fail: boolean;
let errors: ReturnType<typeof vi.spyOn>;
let responseReceived: Promise<void> | undefined;
let introspectionReceived: Promise<void> | undefined;
let grantedScope: string;
let introspectionStatus: number;
let introspectionGate: Promise<void> | undefined;
let releaseIntrospection: (() => void) | undefined;
let putResponseGate: Promise<void> | undefined;
let releasePutResponse: (() => void) | undefined;

beforeEach(async () => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  errors = vi.spyOn(console, "error").mockImplementation(() => {});
  saved = [];
  fail = false;
  responseReceived = undefined;
  introspectionReceived = undefined;
  grantedScope = "atproto";
  introspectionStatus = 200;
  introspectionGate = undefined;
  releaseIntrospection = undefined;
  putResponseGate = undefined;
  releasePutResponse = undefined;
  server = createServer(async (request, response) => {
    const chunks: Buffer[] = [];
    for await (const chunk of request) chunks.push(Buffer.from(chunk));
    if (request.url === "/oauth/introspect") {
      await introspectionGate;
      response.writeHead(introspectionStatus, {
        "content-type": "application/json",
      });
      response.end(JSON.stringify({ active: true, scope: grantedScope }));
      return;
    }
    if (fail) {
      response.writeHead(503, { "content-type": "application/json" });
      response.end(
        JSON.stringify({ error: "Unavailable", message: "Try again" }),
      );
      return;
    }
    saved.push({
      path: request.url,
      ...JSON.parse(Buffer.concat(chunks).toString()),
    });
    if (request.url === "/xrpc/com.atproto.repo.putRecord")
      await putResponseGate;
    response.writeHead(200, { "content-type": "application/json" });
    response.end(JSON.stringify({ uri, cid: savedCid }));
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  const address = server.address();
  if (!address || typeof address === "string")
    throw new Error("Server did not bind");
  const origin = `http://127.0.0.1:${address.port}`;
  const agent = new StreamplaceAgent({
    did,
    fetchHandler: (path, init) => {
      const response = fetch(new URL(path, origin), init);
      responseReceived = response.then(async (result) => {
        await result.clone().arrayBuffer();
      });
      if (String(path) === "/oauth/introspect")
        introspectionReceived = responseReceived;
      return response;
    },
  });
  useStore.setState({
    ...initialState,
    pdsAgent: agent,
    url: "https://node.example",
  });
  store = makeLivestreamStore();
  store.setState({
    segment: {} as NonNullable<ReturnType<typeof store.getState>["segment"]>,
  });
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
});

afterEach(async () => {
  await act(async () => {
    releaseIntrospection?.();
    releasePutResponse?.();
    await introspectionReceived;
    await responseReceived;
  });
  await act(async () => root.unmount());
  container.remove();
  toast.dismiss();
  useStore.setState(initialState, true);
  await new Promise<void>((resolve, reject) =>
    server.close((error) => (error ? reject(error) : resolve())),
  );
  errors.mockRestore();
  vi.unstubAllGlobals();
});

it("waits for a slow permission lookup without losing the selected announcement post", async () => {
  introspectionGate = new Promise<void>((resolve) => {
    releaseIntrospection = resolve;
  });
  grantedScope = "atproto repo?collection=app.bsky.feed.post&action=create";
  const agent = useStore.getState().pdsAgent!;
  useStore.setState({
    oauthSession: {
      did,
      fetchHandler: agent.fetchHandler.bind(agent),
    } as unknown as OAuthSession,
  });
  await render();
  await change(container.querySelector("textarea")!, "A stream");
  expect(checkbox("Create Bluesky post").checked).toBe(true);
  expect(submit().disabled).toBe(true);
  expect(saved).toEqual([]);
  await act(async () => {
    releaseIntrospection!();
    await responseReceived;
  });
  await save();
  expect(saved[0].createBlueskyPost).toBe(true);
});

it("allows an explicit post opt-out while the permission lookup is pending", async () => {
  introspectionGate = new Promise<void>((resolve) => {
    releaseIntrospection = resolve;
  });
  const agent = useStore.getState().pdsAgent!;
  useStore.setState({
    oauthSession: {
      did,
      fetchHandler: agent.fetchHandler.bind(agent),
    } as unknown as OAuthSession,
  });
  await render();
  await change(container.querySelector("textarea")!, "A stream");
  await act(async () => checkbox("Create Bluesky post").click());
  expect(submit().disabled).toBe(false);
  await save();
  expect(saved[0].createBlueskyPost).toBe(false);
});

it("uses current settings for untouched fields and retains explicitly edited fields across saves", async () => {
  const record = {
    $type: "place.stream.livestream" as const,
    title: "Original",
    createdAt: "2026-11-01T12:00:00Z" as const,
    canonicalUrl: "https://old.example/live",
    notificationSettings: { pushNotification: true },
    idleTimeoutSeconds: 300,
  };
  store.setState({
    livestream: {
      uri,
      cid,
      record,
      author: { did, handle: "streamer.example" },
      indexedAt: record.createdAt,
    } as NonNullable<ReturnType<typeof store.getState>["livestream"]>,
  });
  await render();
  await act(async () =>
    store.setState((state) => ({
      livestream: {
        ...state.livestream!,
        record: {
          ...record,
          canonicalUrl: "https://new.example/live",
          notificationSettings: {
            pushNotification: false,
            futurePreference: "keep",
          },
          idleTimeoutSeconds: 900,
        },
      },
    })),
  );
  expect(canonical().value).toBe("https://new.example/live");
  expect(checkbox("Send push notification").checked).toBe(false);
  await change(container.querySelector("textarea")!, "Title only");
  await save();
  expect(saved[0].record).toMatchObject({
    canonicalUrl: "https://new.example/live",
    notificationSettings: { pushNotification: false, futurePreference: "keep" },
    idleTimeoutSeconds: 900,
  });
  await change(canonical(), "https://mine.example/live");
  await act(async () => checkbox("Send push notification").click());
  await act(async () =>
    store.setState((state) => ({
      livestream: {
        ...state.livestream!,
        record: {
          ...state.livestream!.record,
          canonicalUrl: "https://other.example/live",
        },
      },
    })),
  );
  await save();
  expect(saved[1].record).toMatchObject({
    canonicalUrl: "https://mine.example/live",
    notificationSettings: { pushNotification: true, futurePreference: "keep" },
  });
  await act(async () =>
    store.setState((state) => ({
      livestream: {
        ...state.livestream!,
        record: {
          ...state.livestream!.record,
          canonicalUrl: "https://latest.example/live",
          notificationSettings: { pushNotification: false },
        },
      },
    })),
  );
  await change(container.querySelector("textarea")!, "Another title");
  await save();
  expect(saved[2].record).toMatchObject({
    canonicalUrl: "https://latest.example/live",
    notificationSettings: { pushNotification: false },
  });
});

async function render() {
  await act(async () =>
    root.render(
      <DashboardStoreContext.Provider value={store}>
        <StreamInfoWidget store={store} />
      </DashboardStoreContext.Provider>,
    ),
  );
}

function checkbox(text: string) {
  const label = Array.from(container.querySelectorAll("label")).find(
    (element) => element.textContent?.includes(text),
  );
  const input = label?.querySelector<HTMLInputElement>(
    'input[type="checkbox"]',
  );
  expect(input).toBeDefined();
  return input!;
}

function canonical() {
  const input = container.querySelector<HTMLInputElement>('input[type="url"]');
  expect(input).not.toBeNull();
  return input!;
}

async function change(
  element: HTMLInputElement | HTMLTextAreaElement,
  value: string,
) {
  await act(async () => {
    const prototype =
      element instanceof HTMLTextAreaElement
        ? HTMLTextAreaElement.prototype
        : HTMLInputElement.prototype;
    Object.getOwnPropertyDescriptor(prototype, "value")!.set!.call(
      element,
      value,
    );
    element.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

function submit() {
  const button = Array.from(container.querySelectorAll("button")).find(
    (element) =>
      /Start Livestream|Update Livestream/.test(element.textContent ?? ""),
  );
  expect(button).toBeDefined();
  return button!;
}

async function save() {
  responseReceived = undefined;
  await act(async () => {
    submit().click();
    await vi.waitFor(() => expect(responseReceived).toBeDefined());
    await responseReceived;
  });
  expect(submit().disabled).toBe(false);
}

it("sends an explicit push preference and trimmed canonical URL when announcing", async () => {
  await render();
  expect(checkbox("Send push notification").checked).toBe(true);
  await change(container.querySelector("textarea")!, "A stream");
  await change(canonical(), "  https://watch.example/live?channel=one  ");
  await act(async () => checkbox("Send push notification").click());
  await act(async () => checkbox("Create Bluesky post").click());
  await save();
  expect(saved).toHaveLength(1);
  expect(saved[0]).toMatchObject({
    path: "/xrpc/place.stream.live.startLivestream",
    createBlueskyPost: false,
    livestream: {
      title: "A stream",
      url: `https://node.example/${did}`,
      canonicalUrl: "https://watch.example/live?channel=one",
      notificationSettings: { pushNotification: false },
    },
  });
  expect(errors).not.toHaveBeenCalled();
});

it("uses the node default when no canonical URL is specified", async () => {
  await render();
  await change(container.querySelector("textarea")!, "A stream");
  await save();
  expect(saved[0].livestream).not.toHaveProperty("canonicalUrl");
  expect(saved[0].livestream.notificationSettings).toEqual({
    pushNotification: true,
  });
});

it("does not replace a newer record with an older save acknowledgement", async () => {
  const record = {
    $type: "place.stream.livestream" as const,
    title: "Original",
    createdAt: "2026-11-01T12:00:00Z" as const,
    canonicalUrl: "https://original.example/live" as const,
  };
  store.setState({
    livestream: {
      uri,
      cid,
      record,
      author: { did, handle: "streamer.example" },
      indexedAt: record.createdAt,
    } as NonNullable<ReturnType<typeof store.getState>["livestream"]>,
  });
  putResponseGate = new Promise<void>((resolve) => {
    releasePutResponse = resolve;
  });
  await render();
  await change(canonical(), "https://mine.example/live");
  await act(async () => submit().click());
  await vi.waitFor(() => expect(saved).toHaveLength(1));
  const newerCid =
    "bafyreic2o5zat7u4foe4slvdkd3wmhdtd2mvbc66m7lnk7ihw3yxhu3iqe";
  await act(async () =>
    store.setState((state) => ({
      livestream: {
        ...state.livestream!,
        cid: newerCid,
        record: {
          ...state.livestream!.record,
          canonicalUrl: "https://newer.example/live",
        },
      },
    })),
  );
  await act(async () => {
    releasePutResponse!();
    await responseReceived;
  });
  expect(store.getState().livestream?.cid).toBe(newerCid);
  expect(canonical().value).toBe("https://newer.example/live");
});

it("loads stored options and preserves other record fields when updating or clearing the URL", async () => {
  const record = {
    $type: "place.stream.livestream" as const,
    title: "Original",
    createdAt: "2026-11-01T12:00:00Z",
    lastSeenAt: "2026-11-01T12:30:00Z",
    url: "https://origin.example/live",
    canonicalUrl: "https://watch.example/live",
    notificationSettings: { pushNotification: false, futurePreference: "keep" },
    idleTimeoutSeconds: 900,
    agent: "OBS/31",
    post: { uri, cid },
    thumb: {
      $type: "blob" as const,
      ref: BlobRef.fromJsonRef({ cid, mimeType: "image/jpeg" }).ref,
      mimeType: "image/jpeg",
      size: 12,
    },
    futureField: "keep",
  };
  store.setState({
    livestream: {
      uri,
      cid,
      record,
      author: { did, handle: "streamer.example" },
      indexedAt: record.createdAt,
    } as NonNullable<ReturnType<typeof store.getState>["livestream"]>,
  });
  await render();
  expect(canonical().value).toBe(record.canonicalUrl);
  expect(checkbox("Send push notification").checked).toBe(false);
  await change(container.querySelector("textarea")!, "Updated");
  await change(canonical(), "   ");
  await act(async () => checkbox("Send push notification").click());
  await save();
  expect(saved[0]).toMatchObject({
    path: "/xrpc/com.atproto.repo.putRecord",
    swapRecord: cid,
    repo: did,
    rkey: "one",
    record: {
      title: "Updated",
      createdAt: record.createdAt,
      lastSeenAt: record.lastSeenAt,
      url: record.url,
      thumb: { ...record.thumb, ref: { $link: cid } },
      idleTimeoutSeconds: 900,
      agent: record.agent,
      post: record.post,
      futureField: "keep",
      notificationSettings: {
        pushNotification: true,
        futurePreference: "keep",
      },
    },
  });
  expect(saved[0].record).not.toHaveProperty("canonicalUrl");
  expect(store.getState().livestream?.cid).toBe(savedCid);
});

it("starts a fresh record after an ended stream without copying its post or lifecycle fields", async () => {
  const record = {
    $type: "place.stream.livestream" as const,
    title: "Ended stream",
    createdAt: "2026-11-01T12:00:00Z",
    endedAt: "2026-11-01T13:00:00Z",
    canonicalUrl: "https://watch.example/live",
    notificationSettings: { pushNotification: false },
    post: { uri, cid },
  };
  store.setState({
    livestream: {
      uri,
      cid,
      record,
      author: { did, handle: "streamer.example" },
      indexedAt: record.createdAt,
    } as NonNullable<ReturnType<typeof store.getState>["livestream"]>,
  });
  await render();
  await change(container.querySelector("textarea")!, "New stream");
  await save();
  expect(saved[0].path).toBe("/xrpc/place.stream.live.startLivestream");
  expect(saved[0].livestream).toMatchObject({
    title: "New stream",
    url: `https://node.example/${did}`,
    canonicalUrl: record.canonicalUrl,
    notificationSettings: { pushNotification: false },
  });
  expect(saved[0].livestream.createdAt).not.toBe(record.createdAt);
  expect(saved[0].livestream).not.toHaveProperty("endedAt");
  expect(saved[0].livestream).not.toHaveProperty("post");
});

it.each([
  "javascript:alert(1)",
  "/relative",
  "not a URL",
  "https://[invalid",
  "https:example.com",
])("blocks invalid canonical URL %s before writing", async (value) => {
  await render();
  await change(container.querySelector("textarea")!, "A stream");
  await change(canonical(), value);
  expect(submit().disabled).toBe(true);
  expect(saved).toEqual([]);
});

it("keeps entered options after a failed write so saving can be retried", async () => {
  await render();
  await change(container.querySelector("textarea")!, "A stream");
  await change(canonical(), "https://watch.example/live");
  await act(async () => checkbox("Send push notification").click());
  fail = true;
  await save();
  expect(saved).toHaveLength(0);
  expect(canonical().value).toBe("https://watch.example/live");
  expect(checkbox("Send push notification").checked).toBe(false);
  expect(errors).toHaveBeenCalledTimes(1);
  fail = false;
  await save();
  expect(saved[0].livestream.canonicalUrl).toBe("https://watch.example/live");
  expect(saved[0].livestream.notificationSettings.pushNotification).toBe(false);
});

async function renderWithScope(scope: string) {
  grantedScope = scope;
  responseReceived = undefined;
  const agent = useStore.getState().pdsAgent!;
  await act(async () =>
    useStore.setState({
      oauthSession: {
        did,
        fetchHandler: agent.fetchHandler.bind(agent),
      } as unknown as OAuthSession,
    }),
  );
  await render();
  await act(async () => {
    await vi.waitFor(() => expect(responseReceived).toBeDefined());
    await responseReceived;
  });
}

it("explains denied Bluesky posting permission without disabling notifications or announcement", async () => {
  await renderWithScope(
    "atproto repo?collection=app.bsky.feed.post&action=delete",
  );
  expect(checkbox("Create Bluesky post").disabled).toBe(true);
  expect(checkbox("Create Bluesky post").checked).toBe(false);
  expect(container.textContent).toContain(
    "you haven't given us permission to post Bluesky posts for you",
  );
  expect(container.textContent).toContain(
    "sign in again with Bluesky access enabled to change this.",
  );
  expect(checkbox("Send push notification").disabled).toBe(false);
  await change(container.querySelector("textarea")!, "A stream");
  await save();
  expect(saved[0].createBlueskyPost).toBe(false);
  expect(saved[0].livestream.notificationSettings.pushNotification).toBe(true);
});

it("enables posting without the permission warning when create permission was granted", async () => {
  await renderWithScope(
    "atproto repo?collection=app.bsky.feed.post&action=create",
  );
  expect(checkbox("Create Bluesky post").disabled).toBe(false);
  expect(checkbox("Create Bluesky post").checked).toBe(true);
  expect(container.textContent).not.toContain(
    "you haven't given us permission",
  );
});

it("keeps older nodes usable when introspection is unavailable", async () => {
  introspectionStatus = 404;
  await renderWithScope("atproto");
  expect(checkbox("Create Bluesky post").disabled).toBe(false);
  expect(container.textContent).not.toContain(
    "you haven't given us permission",
  );
});

it("updates the permission message after a new sign-in", async () => {
  await renderWithScope("atproto");
  expect(checkbox("Create Bluesky post").disabled).toBe(true);
  await renderWithScope(
    "atproto repo?collection=app.bsky.feed.post&action=create",
  );
  expect(checkbox("Create Bluesky post").disabled).toBe(false);
  expect(checkbox("Create Bluesky post").checked).toBe(true);
  expect(container.textContent).not.toContain(
    "you haven't given us permission",
  );
});
