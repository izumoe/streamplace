import {
  OAuthSession,
  type BrowserOAuthClient,
} from "@atproto/oauth-client-browser";
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
import { Route } from "./access";

const { warnings } = vi.hoisted(() => ({
  warnings: vi.spyOn(console, "warn").mockImplementation(() => {}),
}));
vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (key: string, fallback?: string) => fallback ?? key,
  }),
}));
const initialState = useStore.getState();
let root: ReturnType<typeof createRoot>;
let container: HTMLDivElement;
let admin = true;
let revoked = false;
let requests: { method: string; input?: Record<string, unknown> }[];
const subject = "did:plc:publisher";
const serverMetadata: OAuthSession["serverMetadata"] = {
  issuer: "https://node.example",
  authorization_endpoint: "https://node.example/oauth/authorize",
  token_endpoint: "https://node.example/oauth/token",
  token_endpoint_auth_methods_supported: ["private_key_jwt"],
};

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
  notifyManager.setNotifyFunction((callback) => {
    act(callback);
  });
  admin = true;
  revoked = false;
  requests = [];
  const session: OAuthSession = vi.mockObject(OAuthSession.prototype);
  vi.spyOn(session, "did", "get").mockReturnValue("did:plc:admin");
  vi.spyOn(session, "serverMetadata", "get").mockReturnValue(serverMetadata);

  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = new Request(input, init);
      const method = new URL(request.url).pathname.split(".").at(-1)!;
      const body = request.method === "POST" ? await request.json() : undefined;
      requests.push({ method, input: body });
      const decisions = ["admin", "streamer", "chat"].map((capability) => ({
        capability,
        mode: capability === "chat" ? "open" : "allowlist",
        allowed: capability === "admin" && admin,
        enforced: true,
        reason: "test node policy",
      }));
      if (method === "getStatus")
        return Response.json({ revision: 1, capabilities: decisions });
      const base = {
        authority: "did:web:node.example",
        subject,
        capability: "streamer",
        reason: "operator decision",
      };
      const allowance = {
        ...base,
        id: "allow-id",
        effect: "allow",
        source: "stored",
        effectiveAllowed: revoked,
      };
      const denial = {
        ...base,
        id: "deny-id",
        effect: "deny",
        source: "stored",
        effectiveAllowed: false,
      };
      const configured = {
        ...base,
        id: "config-id",
        subject: "did:plc:configured",
        effect: "allow",
        source: "configuration",
        effectiveAllowed: true,
      };
      if (method === "listGrants")
        return Response.json({
          revision: 1,
          grants: revoked
            ? [allowance, configured]
            : [allowance, denial, configured],
        });
      if (method === "deleteGrant") {
        revoked = true;
        return Response.json({ success: true });
      }
      return Response.json(allowance);
    },
  );
  useStore.setState({
    ...initialState,
    url: "https://node.example",
    oauthSession: session,
    client: {
      clientMetadata: { redirect_uris: ["https://node.example/login"] },
    } as unknown as BrowserOAuthClient,
    pdsAgent: new StreamplaceAgent("https://node.example"),
  });
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
});
afterEach(async () => {
  await act(async () => root.unmount());
  notifyManager.setNotifyFunction((callback) => callback());
  container.remove();
  useStore.setState(initialState, true);
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});
async function render() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  await act(async () =>
    root.render(
      createElement(
        QueryClientProvider,
        { client },
        createElement(Route.options.component as ComponentType),
      ),
    ),
  );
}
async function expectText(text: string) {
  await vi.waitFor(async () => {
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    expect(container.textContent).toContain(text);
  });
}

it("does not fetch administrator entries or expose controls to another account", async () => {
  admin = false;
  await render();
  await expectText("Node administrator access is required");
  expect(container.querySelector("form")).toBeNull();
  expect(requests.map((request) => request.method)).not.toContain("listGrants");
});
it("shows effective denials and revokes only their exact entry without editing configuration", async () => {
  await render();
  await expectText("Publishing denied");
  const config = container.querySelector('[data-entry-id="config-id"]')!;
  expect((config.querySelector("button") as HTMLButtonElement).disabled).toBe(
    true,
  );
  const denial = container.querySelector('[data-entry-id="deny-id"]')!;
  await act(async () =>
    (denial.querySelector("button") as HTMLButtonElement).click(),
  );
  await expectText("Publishing permitted");
  expect(
    requests.find((request) => request.method === "deleteGrant")?.input,
  ).toEqual({ id: "deny-id" });
  expect(container.querySelector('[data-entry-id="deny-id"]')).toBeNull();
  expect(
    container.querySelector('[data-entry-id="allow-id"]')?.textContent,
  ).toContain("Publishing permitted");
});

it("does not send administration requests through a sign-in for a different selected node", async () => {
  useStore.setState({ url: "https://another-node.example" });
  await render();
  await expectText("Reload to sign in to this node before managing access.");
  expect(container.querySelector("form")).toBeNull();
  expect(requests).toEqual([]);
});

it.each([38080, 38081])(
  "checks the canonical loopback session issuer port %s",
  async (port) => {
    useStore.setState({
      url: "http://localhost:38080",
      pdsAgent: new StreamplaceAgent("http://localhost:38080"),
      client: {
        clientMetadata: { redirect_uris: [`http://127.0.0.1:${port}/login`] },
      } as unknown as BrowserOAuthClient,
    });
    vi.spyOn(
      useStore.getState().oauthSession!,
      "serverMetadata",
      "get",
    ).mockReturnValue({
      ...serverMetadata,
      issuer: `http://127.0.0.1:${port}`,
    });
    await render();
    if (port === 38080) await expectText("Publishing denied");
    else {
      await expectText(
        "Reload to sign in to this node before managing access.",
      );
      expect(requests).toEqual([]);
    }
  },
);

it("uses the session issuer rather than a client's allowed callback origins", async () => {
  useStore.setState({
    url: "https://another-node.example",
    client: {
      clientMetadata: {
        redirect_uris: [
          "https://node.example/login",
          "https://another-node.example/login",
        ],
      },
    } as unknown as BrowserOAuthClient,
  });
  await render();
  await expectText("Reload to sign in to this node before managing access.");
  expect(requests).toEqual([]);
});
