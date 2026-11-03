import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { StreamplaceAgent } from "streamplace";
import {
  afterAll,
  afterEach,
  beforeEach,
  describe,
  expect,
  it,
  vi,
} from "vitest";
import { useStore } from "../lib/store";
import { useBio } from "./use-bio";

const compatibilityWarnings = vi.hoisted(() =>
  vi.spyOn(console, "warn").mockImplementation(() => {}),
);
afterAll(() => {
  const messages = compatibilityWarnings.mock.calls.map(([message]) => message);
  compatibilityWarnings.mockRestore();
  expect(
    messages.filter(
      (message) =>
        message !==
          "[@atproto/lex-data]: Uint8Array.fromBase64 / Uint8Array.prototype.toBase64 not available in this environment. Falling back to ponyfill implementation." &&
        message !==
          "Locks API not available. You should consider using a more recent browser.",
    ),
  ).toEqual([]);
});

const did = "did:plc:bio-owner";
const firstNode = "https://first-node.test";
const secondNode = "https://second-node.test";

function bio(plaintext: string) {
  return {
    $type: "place.stream.bio.page",
    createdAt: "2026-11-03T00:00:00Z",
    description: { plaintext },
  };
}

describe("useBio", () => {
  let root: Root;
  let client: QueryClient;
  let current: ReturnType<typeof useBio>;
  let requests: URL[];
  let response: (url: URL) => Response;
  let previous: Pick<
    ReturnType<typeof useStore.getState>,
    "url" | "anonPDSAgent"
  >;

  function Probe({ repo }: { repo?: string }) {
    current = useBio(repo);
    return null;
  }
  async function render(repo?: string) {
    await act(async () => {
      root.render(
        <QueryClientProvider client={client}>
          <Probe repo={repo} />
        </QueryClientProvider>,
      );
    });
  }
  async function settled() {
    await act(async () => {
      await vi.waitFor(() => expect(current.isFetching).toBe(false));
    });
  }
  beforeEach(() => {
    vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
    previous = {
      url: useStore.getState().url,
      anonPDSAgent: useStore.getState().anonPDSAgent,
    };
    useStore.setState({
      url: firstNode,
      anonPDSAgent: new StreamplaceAgent(firstNode),
    });
    requests = [];
    response = () => Response.json(bio("about the streamer"));
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>((input) => {
        const url = new URL(
          input instanceof Request ? input.url : input.toString(),
        );
        requests.push(url);
        expect(url.pathname).toBe("/xrpc/place.stream.bio.getPage");
        expect(url.searchParams.get("repo")).toBe(did);
        return Promise.resolve(response(url));
      }),
    );
    client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    root = createRoot(document.createElement("div"));
  });
  afterEach(async () => {
    await act(async () => root.unmount());
    client.clear();
    useStore.setState(previous);
    vi.unstubAllGlobals();
  });
  it("waits for the account before making a public request", async () => {
    await render();
    expect(requests).toHaveLength(0);
    await render(did);
    await settled();
    expect(current.data?.description?.plaintext).toBe("about the streamer");
    expect(requests).toHaveLength(1);
  });
  it("treats missing and moderated profiles as absent without retries", async () => {
    response = () =>
      Response.json(
        { error: "BioNotFound", message: "Bio page not found" },
        { status: 404 },
      );
    await render(did);
    await settled();
    expect(current.isSuccess).toBe(true);
    expect(current.data).toBeNull();
    expect(requests).toHaveLength(1);
  });
  it("keeps an unavailable node distinct from an absent bio", async () => {
    response = () =>
      Response.json(
        { error: "InternalServerError", message: "database unavailable" },
        { status: 503 },
      );
    await render(did);
    await settled();
    expect(current.isError).toBe(true);
    expect(current.data).toBeUndefined();
  });
  it("fetches from the selected node when its cached anonymous agent still names another node", async () => {
    response = (url) => Response.json(bio(url.origin));
    await render(did);
    await settled();
    expect(current.data?.description?.plaintext).toBe(firstNode);
    await act(async () => useStore.setState({ url: secondNode }));
    await settled();
    expect(current.data?.description?.plaintext).toBe(secondNode);
    expect(requests.map((url) => url.origin)).toEqual([firstNode, secondNode]);
  });
});
