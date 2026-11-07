import { OAuthSession } from "@atproto/oauth-client-browser";
import {
  notifyManager,
  QueryClient,
  QueryClientProvider,
} from "@tanstack/react-query";
import { act, createElement, type ComponentType } from "react";
import { createRoot } from "react-dom/client";
import { StreamplaceAgent } from "streamplace";
import { afterEach, beforeAll, beforeEach, expect, it, vi } from "vitest";
import { useStore } from "../../lib/store";
import { Route } from "./branding";

const { warnings } = vi.hoisted(() => ({
  warnings: vi.spyOn(console, "warn").mockImplementation(() => {}),
}));
vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (
      key: string,
      fallback?:
        | string
        | {
            defaultValue?: string;
            number?: number;
            size?: number;
            kind?: string;
          },
    ) =>
      typeof fallback === "string"
        ? fallback
        : (fallback?.defaultValue ?? key)
            .replace("{{kind}}", String(fallback?.kind))
            .replace("{{number}}", String(fallback?.number))
            .replace("{{size}}", String(fallback?.size)),
  }),
}));
const initialState = useStore.getState();
let root: ReturnType<typeof createRoot>;
let container: HTMLDivElement;
let queryClient: QueryClient;
let admin: boolean;
let failSave: boolean;
let failLoad: boolean;
let reads: number;
let writes: string[];
let assets: Map<
  string,
  { key: string; mimeType: string; data?: string; url?: string }
>;

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
beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  notifyManager.setNotifyFunction((callback) => act(callback));
  admin = true;
  failSave = false;
  failLoad = false;
  reads = 0;
  writes = [];
  assets = new Map([
    [
      "siteTitle",
      { key: "siteTitle", mimeType: "text/plain", data: "Existing node" },
    ],
    [
      "primaryColor",
      { key: "primaryColor", mimeType: "text/plain", data: "#123456" },
    ],
    [
      "socialLinks",
      {
        key: "socialLinks",
        mimeType: "text/plain",
        data: JSON.stringify([
          {
            label: "Community",
            url: "https://community.example",
            icon: "discord",
          },
        ]),
      },
    ],
    [
      "legalLinks",
      {
        key: "legalLinks",
        mimeType: "text/plain",
        data: '[{"text":"Privacy","url":"https://node.example/privacy"}]',
      },
    ],
    [
      "mainLogo",
      {
        key: "mainLogo",
        mimeType: "image/png",
        url: "/xrpc/place.stream.branding.getBlob?key=mainLogo",
      },
    ],
  ]);
  const session: OAuthSession = vi.mockObject(OAuthSession.prototype);
  vi.spyOn(session, "did", "get").mockReturnValue("did:plc:admin");
  vi.spyOn(session, "serverMetadata", "get").mockReturnValue({
    issuer: "https://node.example",
    authorization_endpoint: "https://node.example/oauth/authorize",
    token_endpoint: "https://node.example/oauth/token",
    token_endpoint_auth_methods_supported: ["private_key_jwt"],
  });
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = new Request(input, init);
      const method = new URL(request.url).pathname.split(".").at(-1);
      if (method === "getStatus")
        return Response.json({
          revision: 1,
          capabilities: [
            {
              capability: "admin",
              mode: "allowlist",
              allowed: admin,
              enforced: true,
              reason: "node policy",
            },
          ],
        });
      if (method === "getBranding") {
        reads++;
        return failLoad
          ? Response.json(
              { error: "Unavailable", message: "Cannot load branding" },
              { status: 503 },
            )
          : Response.json({ assets: [...assets.values()] });
      }
      if (request.method === "POST") {
        const body = await request.json();
        writes.push(body.key);
        if (failSave || !admin)
          return Response.json(
            { error: "Forbidden", message: "Cannot save branding" },
            { status: 403 },
          );
        if (method === "deleteBlob") assets.delete(body.key);
        else
          assets.set(body.key, {
            key: body.key,
            mimeType: body.mimeType,
            ...(body.mimeType === "text/plain"
              ? { data: Buffer.from(body.data, "base64").toString("utf8") }
              : { url: `/xrpc/place.stream.branding.getBlob?key=${body.key}` }),
          });
        return Response.json({ success: true });
      }
      throw new Error(`Unexpected request: ${request.url}`);
    },
  );
  useStore.setState({
    ...initialState,
    url: "https://node.example",
    oauthSession: session,
    pdsAgent: new StreamplaceAgent("https://node.example"),
    broadcasterDID: "did:web:node.example",
    // The page must refresh the public branding after a write, without downloading images in these DOM tests.
    fetchBranding: async () => true,
  });
  queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
});
afterEach(async () => {
  await act(async () => root.unmount());
  queryClient.clear();
  notifyManager.setNotifyFunction((callback) => callback());
  container.remove();
  useStore.setState(initialState, true);
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});
async function render() {
  await act(async () =>
    root.render(
      createElement(
        QueryClientProvider,
        { client: queryClient },
        createElement(Route.options.component as ComponentType),
      ),
    ),
  );
}
async function loaded() {
  await vi.waitFor(async () => {
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    expect(
      container.querySelector<HTMLInputElement>('[aria-label="Site name"]')
        ?.value,
    ).toBe("Existing node");
  });
}
function button(name: string) {
  const result = Array.from(container.querySelectorAll("button")).find(
    (b) => b.textContent === name || b.getAttribute("aria-label") === name,
  );
  expect(result, name).toBeTruthy();
  return result!;
}
async function edit(label: string, value: string) {
  const input = container.querySelector<HTMLInputElement>(
    `[aria-label="${label}"]`,
  )!;
  expect(input, label).toBeTruthy();
  await act(async () => {
    Object.getOwnPropertyDescriptor(
      HTMLInputElement.prototype,
      "value",
    )!.set!.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
}
async function click(name: string) {
  await act(async () => button(name).click());
}

it("loads the node's saved values and image previews, exposing only the chosen subset", async () => {
  await render();
  await loaded();
  expect(
    container.querySelector<HTMLImageElement>('img[alt="Logo"]')?.src,
  ).toContain("https://node.example/xrpc/");
  expect(
    container.querySelector<HTMLInputElement>(
      '[aria-label="Social link 1 label"]',
    )?.value,
  ).toBe("Community");
  expect(container.textContent).not.toMatch(
    /Primary color|Default streamer|Broadcaster DID|Sidebar background/,
  );
});
it.each(["non-admin", "signed-out", "wrong-node"])(
  "does not expose the editor or load its data for %s",
  async (mode) => {
    if (mode === "non-admin") admin = false;
    if (mode === "signed-out") useStore.setState({ oauthSession: null });
    if (mode === "wrong-node")
      useStore.setState({ url: "https://other.example" });
    await render();
    await vi.waitFor(() =>
      expect(container.textContent).toMatch(/Sign in|administrator|this node/),
    );
    expect(container.querySelector("form")).toBeNull();
    expect(reads).toBe(0);
    expect(writes).toEqual([]);
  },
);
it("preserves Unicode and unrelated keys when saving a field, and keeps the draft on failure", async () => {
  await render();
  await loaded();
  await edit("Site name", "配信 café");
  failSave = true;
  await click("Save site name");
  await vi.waitFor(() =>
    expect(container.querySelector('[role="alert"]')?.textContent).toContain(
      "Cannot save branding",
    ),
  );
  expect(assets.get("siteTitle")?.data).toBe("Existing node");
  expect(
    container.querySelector<HTMLInputElement>('[aria-label="Site name"]')
      ?.value,
  ).toBe("配信 café");
  failSave = false;
  await click("Save site name");
  await vi.waitFor(() =>
    expect(assets.get("siteTitle")?.data).toBe("配信 café"),
  );
  expect(assets.get("primaryColor")?.data).toBe("#123456");
});
it("preserves existing social icons and saves an empty link list explicitly", async () => {
  await render();
  await loaded();
  await edit("Social link 1 label", "Our community");
  await click("Save social links");
  await vi.waitFor(() =>
    expect(JSON.parse(assets.get("socialLinks")!.data!)[0]).toEqual({
      label: "Our community",
      url: "https://community.example",
      icon: "discord",
    }),
  );
  await click("Remove Social link 1");
  await click("Save social links");
  await vi.waitFor(() => expect(assets.get("socialLinks")?.data).toBe("[]"));
  expect(assets.get("legalLinks")?.data).toContain("Privacy");
});
it("rejects unsafe links before writing and retains them for correction", async () => {
  await render();
  await loaded();
  await edit("Social link 1 URL", "javascript:alert(1)");
  await click("Save social links");
  expect(writes).toEqual([]);
  expect(container.textContent).toContain("Use an http or https URL");
});
it("resets only the selected asset and removes its preview after success", async () => {
  await render();
  await loaded();
  await click("Reset logo");
  await vi.waitFor(() =>
    expect(container.querySelector('img[alt="Logo"]')).toBeNull(),
  );
  expect(assets.has("mainLogo")).toBe(false);
  expect(assets.has("siteTitle")).toBe(true);
});
it("shows a loading failure and allows a retry before presenting editable fields", async () => {
  failLoad = true;
  await render();
  await vi.waitFor(() =>
    expect(container.textContent).toContain("Cannot load branding"),
  );
  expect(container.querySelector("form")).toBeNull();
  failLoad = false;
  await click("Retry");
  await loaded();
});
it("discards drafts when the selected node no longer matches the sign-in", async () => {
  await render();
  await loaded();
  await edit("Site name", "Do not send this elsewhere");
  await act(async () => useStore.setState({ url: "https://other.example" }));
  expect(container.querySelector("form")).toBeNull();
  expect(writes).toEqual([]);
});

it("rejects oversized image files before reading or sending them", async () => {
  await render();
  await loaded();
  const input = container.querySelector<HTMLInputElement>(
    'input[type="file"][aria-label="Favicon"]',
  )!;
  const file = new File([new Uint8Array(100 * 1024 + 1)], "large.png", {
    type: "image/png",
  });
  Object.defineProperty(input, "files", { value: [file] });
  await act(async () =>
    input.dispatchEvent(new Event("change", { bubbles: true })),
  );
  expect(container.textContent).toContain("within the size limit");
  expect(writes).toEqual([]);
});
it("keeps malformed legacy link data visible as an error until it is reset", async () => {
  assets.set("socialLinks", {
    key: "socialLinks",
    mimeType: "text/plain",
    data: '{"unexpected":"object"}',
  });
  await render();
  await loaded();
  expect(container.textContent).toContain("Could not read the saved links");
  expect(writes).toEqual([]);
});
