import { useStore } from "@/lib/store";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterAll, afterEach, beforeEach, expect, it, vi } from "vitest";
import { BioBlockView } from "./bio-viewer";

const warnings = vi.hoisted(() =>
  vi.spyOn(console, "warn").mockImplementation(() => {}),
);
afterAll(() => {
  const messages = warnings.mock.calls.map(([message]) => message);
  warnings.mockRestore();
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
const node = "https://node.test";
const pds = "https://owner-pds.test";
let previousUrl: string;
let root: ReturnType<typeof createRoot>;
let container: HTMLDivElement;
let client: QueryClient;
let requests: URL[];
let failBlob: boolean;
const createURL = vi.fn((_blob: Blob) => "blob:test/pds-image");
const revokeURL = vi.fn();

beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  previousUrl = useStore.getState().url;
  useStore.setState({ url: node });
  requests = [];
  failBlob = false;
  createURL.mockClear();
  revokeURL.mockClear();
  vi.stubGlobal(
    "URL",
    class extends URL {
      static createObjectURL = createURL;
      static revokeObjectURL = revokeURL;
    },
  );
  vi.stubGlobal(
    "fetch",
    vi.fn<typeof fetch>(async (input, init) => {
      const request =
        input instanceof Request ? input : new Request(input, init);
      const url = new URL(request.url);
      requests.push(url);
      expect(request.headers.get("authorization")).toBeNull();
      if (url.pathname === "/xrpc/com.atproto.identity.refreshIdentity") {
        expect(url.origin).toBe(useStore.getState().url);
        expect(await request.json()).toEqual({ identifier: did });
        return Response.json({
          did,
          handle: "owner.test",
          didDoc: {
            id: did,
            service: [
              {
                id: "#atproto_pds",
                type: "AtprotoPersonalDataServer",
                serviceEndpoint: pds,
              },
            ],
          },
        });
      }
      expect(url.origin).toBe(pds);
      expect(url.pathname).toBe("/xrpc/com.atproto.sync.getBlob");
      expect(url.searchParams.get("did")).toBe(did);
      expect(url.searchParams.get("cid")).toBe("image-cid");
      return failBlob
        ? new Response("missing", { status: 404 })
        : new Response(new Uint8Array([137, 80, 78, 71]), {
            headers: { "content-type": "image/png" },
          });
    }),
  );
  container = document.createElement("div");
  root = createRoot(container);
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
});
afterEach(async () => {
  await act(async () => root.unmount());
  client.clear();
  useStore.setState({ url: previousUrl });
  vi.unstubAllGlobals();
});
async function render(cid = "image-cid") {
  await act(async () =>
    root.render(
      <QueryClientProvider client={client}>
        <BioBlockView
          did={did}
          block={{
            $type: "place.stream.bio.blocks.image",
            image: { cid, mimeType: "image/png" },
            alt: "profile artwork",
            aspectRatio: { width: 1, height: 1 },
          }}
        />
      </QueryClientProvider>,
    ),
  );
}
async function failCDN() {
  await act(async () =>
    container.querySelector("img")!.dispatchEvent(new Event("error")),
  );
}
it("resolves the owner's PDS only after CDN failure and releases the fallback URL on replacement", async () => {
  await render();
  expect(container.querySelector("img")!.src).toContain("cdn.bsky.app");
  expect(requests).toEqual([]);
  await failCDN();
  await act(async () => {
    await vi.waitFor(() => expect(client.isFetching()).toBe(0));
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
  expect(container.querySelector("img")?.src).toBe("blob:test/pds-image");
  expect(requests).toHaveLength(2);
  const blob = createURL.mock.calls[0][0] as Blob;
  expect(blob.type).toBe("image/png");
  expect(Array.from(new Uint8Array(await blob.arrayBuffer()))).toEqual([
    137, 80, 78, 71,
  ]);
  await render("replacement-cid");
  expect(container.querySelector("img")!.src).toContain("replacement-cid");
  expect(revokeURL).toHaveBeenCalledWith("blob:test/pds-image");
});
it("retains accessible alt text without repeated requests when the PDS blob is unavailable", async () => {
  failBlob = true;
  await render();
  await failCDN();
  await act(async () =>
    vi.waitFor(() => expect(container.textContent).toBe("profile artwork")),
  );
  expect(requests).toHaveLength(2);
  expect(createURL).not.toHaveBeenCalled();
});

it("retries a failed image decode when the selected node supplies a new blob", async () => {
  createURL
    .mockReturnValueOnce("blob:test/first")
    .mockReturnValueOnce("blob:test/second");
  await render();
  await failCDN();
  await act(async () => {
    await vi.waitFor(() => expect(client.isFetching()).toBe(0));
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
  expect(container.querySelector("img")?.src).toBe("blob:test/first");
  await failCDN();
  expect(container.textContent).toBe("profile artwork");
  await act(async () => useStore.setState({ url: "https://second-node.test" }));
  await act(async () => {
    await vi.waitFor(() => expect(client.isFetching()).toBe(0));
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
  expect(container.querySelector("img")?.src).toBe("blob:test/second");
  expect(revokeURL).toHaveBeenCalledWith("blob:test/first");
});
