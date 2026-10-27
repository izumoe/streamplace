import {
  BrowserOAuthClient,
  OAuthSession,
} from "@atproto/oauth-client-browser";
import { StreamplaceAgent } from "streamplace";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createStore } from "zustand/vanilla";
import createOAuthClient from "../../oauth";
import type { AppStore } from "../index";
import { DID_KEY } from "./baseSlice";
import { createBlueskySlice } from "./blueskySlice";

vi.mock("../../oauth", () => ({ default: vi.fn() }));

type OAuthClient = Awaited<ReturnType<typeof createOAuthClient>>;

function makeStore() {
  return createStore<AppStore>()(
    (...args) =>
      ({
        ...createBlueskySlice(...args),
        url: "https://node.example",
      }) as AppStore,
  );
}

describe("OAuth initialization", () => {
  let errorLog: ReturnType<typeof vi.spyOn>;

  beforeEach(() => {
    localStorage.clear();
    vi.mocked(createOAuthClient).mockReset();
    errorLog = vi.spyOn(console, "error").mockImplementation(() => {});
  });

  afterEach(() => vi.restoreAllMocks());

  it("makes public reads available while OAuth initialization is pending", async () => {
    let finish!: (client: OAuthClient) => void;
    vi.mocked(createOAuthClient).mockReturnValue(
      new Promise((resolve) => {
        finish = resolve;
      }),
    );
    const store = makeStore();
    const initializing = store.getState().loadOAuthClient();
    try {
      expect(store.getState().anonPDSAgent).toBeInstanceOf(StreamplaceAgent);
    } finally {
      finish(vi.mockObject(BrowserOAuthClient.prototype));
      await initializing;
    }
    expect(store.getState().authStatus).toBe("loggedOut");
    expect(store.getState().loginState.error).toBeNull();
    expect(errorLog).not.toHaveBeenCalled();
  });

  it("finishes anonymously with a retryable sign-in error when metadata fails", async () => {
    const reason = new Error("meta.scope is required");
    vi.mocked(createOAuthClient).mockRejectedValue(reason);
    const store = makeStore();

    await store.getState().loadOAuthClient();

    expect(store.getState().authStatus).toBe("loggedOut");
    expect(store.getState().anonPDSAgent).toBeInstanceOf(StreamplaceAgent);
    expect(store.getState().pdsAgent).toBeNull();
    expect(store.getState().loginState).toEqual({
      loading: false,
      error: "Sign-in is unavailable. Please try again.",
    });
    expect(errorLog).toHaveBeenCalledWith("loadOAuthClient error", reason);
  });

  it("keeps an authenticated session when refreshing OAuth metadata fails", async () => {
    const reason = new TypeError("Failed to fetch");
    vi.mocked(createOAuthClient).mockRejectedValue(reason);
    const store = makeStore();
    const session = vi.mockObject(OAuthSession.prototype);
    vi.spyOn(session, "did", "get").mockReturnValue("did:plc:viewer");
    const agent = new StreamplaceAgent(session);
    const client = vi.mockObject(BrowserOAuthClient.prototype);
    localStorage.setItem(DID_KEY, session.did);
    store.setState({
      authStatus: "loggedIn",
      oauthSession: session,
      pdsAgent: agent,
      client,
    });

    const initializing = store.getState().loadOAuthClient();
    try {
      expect(store.getState().authStatus).toBe("loggedIn");
    } finally {
      await initializing;
    }

    expect(store.getState().authStatus).toBe("loggedIn");
    expect(store.getState().oauthSession).toBe(session);
    expect(store.getState().pdsAgent).toBe(agent);
    expect(store.getState().client).toBe(client);
    expect(localStorage.getItem(DID_KEY)).toBe(session.did);
    expect(errorLog).toHaveBeenCalledWith("loadOAuthClient error", reason);
  });

  it("keeps the usable initialization error when a login attempt fails", async () => {
    const reason = new Error("meta.scope is required");
    vi.mocked(createOAuthClient).mockRejectedValue(reason);
    const store = makeStore();

    await expect(store.getState().login("viewer.example")).rejects.toThrow(
      "Sign-in is unavailable. Please try again.",
    );

    expect(store.getState().authStatus).toBe("loggedOut");
    expect(store.getState().loginState.loading).toBe(false);
    expect(store.getState().loginState.error).toBe(
      "Sign-in is unavailable. Please try again.",
    );
    expect(errorLog).toHaveBeenCalledWith("loadOAuthClient error", reason);
    expect(errorLog).toHaveBeenCalledWith(
      "login rejected",
      expect.objectContaining({
        message: "Sign-in is unavailable. Please try again.",
      }),
      undefined,
    );
  });

  it("clears the initialization error after a successful retry", async () => {
    vi.mocked(createOAuthClient).mockResolvedValue(
      vi.mockObject(BrowserOAuthClient.prototype),
    );
    const store = makeStore();
    store.getState().setLoginError("Sign-in is unavailable. Please try again.");

    await store.getState().loadOAuthClient();

    expect(store.getState().authStatus).toBe("loggedOut");
    expect(store.getState().loginState.error).toBeNull();
    expect(errorLog).not.toHaveBeenCalled();
  });

  it("clears the authenticated agent when its stored session cannot be restored", async () => {
    const session = vi.mockObject(OAuthSession.prototype);
    vi.spyOn(session, "did", "get").mockReturnValue("did:plc:viewer");
    const client = vi.mockObject(BrowserOAuthClient.prototype);
    const reason = new Error("Session revoked");
    client.restore.mockRejectedValue(reason);
    vi.mocked(createOAuthClient).mockResolvedValue(client);
    const store = makeStore();
    localStorage.setItem(DID_KEY, session.did);
    localStorage.setItem("@@atproto/oauth-client-browser(sub)", session.did);
    store.setState({
      authStatus: "loggedIn",
      oauthSession: session,
      pdsAgent: new StreamplaceAgent(session),
      client,
    });

    await store.getState().loadOAuthClient();

    expect(store.getState().authStatus).toBe("loggedOut");
    expect(store.getState().oauthSession).toBeNull();
    expect(store.getState().pdsAgent).toBeNull();
    expect(store.getState().anonPDSAgent).toBeInstanceOf(StreamplaceAgent);
    expect(localStorage.getItem(DID_KEY)).toBeNull();
    expect(
      localStorage.getItem("@@atproto/oauth-client-browser(sub)"),
    ).toBeNull();
    expect(errorLog).toHaveBeenCalledWith("Error restoring session", reason);
  });
});
