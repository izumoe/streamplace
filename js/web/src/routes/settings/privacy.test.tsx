import { OAuthSession } from "@atproto/oauth-client-browser";
import { act, createElement, type ComponentType } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, expect, it, vi } from "vitest";
import { SessionProvider } from "../../lib/session";
import { useStore } from "../../lib/store";
import { Route } from "./privacy";

const { importWarning } = vi.hoisted(() => ({
  importWarning: vi.spyOn(console, "warn").mockImplementation(() => {}),
}));
vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

let container: HTMLDivElement;
let root: ReturnType<typeof createRoot>;
const initialState = useStore.getState();

beforeAll(() => {
  try {
    expect(importWarning.mock.calls).toHaveLength(2);
    expect(importWarning.mock.calls).toEqual(
      expect.arrayContaining([
        [
          "[@atproto/lex-data]: Uint8Array.fromBase64 / Uint8Array.prototype.toBase64 not available in this environment. Falling back to ponyfill implementation.",
        ],
        [
          "Locks API not available. You should consider using a more recent browser.",
        ],
      ]),
    );
  } finally {
    importWarning.mockRestore();
  }
});

beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  const session = vi.mockObject(OAuthSession.prototype);
  vi.spyOn(session, "did", "get").mockReturnValue("did:plc:streamer");
  useStore.setState({
    ...initialState,
    url: "https://stream.example",
    authStatus: "loggedIn",
    oauthSession: session,
    pdsAgent: null,
    anonPDSAgent: null,
    profiles: {},
    serverSettings: null,
  });
  container = document.createElement("div");
  root = createRoot(container);
});

afterEach(async () => {
  await act(async () => root.unmount());
  useStore.setState(initialState, true);
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

async function render() {
  await act(async () => {
    root.render(
      <SessionProvider>
        {createElement(Route.options.component as ComponentType)}
      </SessionProvider>,
    );
  });
}

it("shows recording consent and publishing controls to a signed-in account without an access lookup", async () => {
  await render();

  expect(container.textContent).toContain("livestream-recording-title");
  expect(container.textContent).toContain("auto-publish-vods-title");
  expect(
    Array.from(container.querySelectorAll('[role="switch"]')).map((control) =>
      control.getAttribute("aria-checked"),
    ),
  ).toEqual(["false", "true", "false"]);

  await act(async () => {
    useStore.setState({
      serverSettings: {
        $type: "place.stream.server.settings",
        livestreamRecording: false,
        autoPublishVods: true,
      },
    });
  });
  expect(
    Array.from(container.querySelectorAll('[role="switch"]')).map((control) =>
      control.getAttribute("aria-checked"),
    ),
  ).toEqual(["false", "false", "true"]);
});

it("keeps privacy controls behind login", async () => {
  useStore.setState({ authStatus: "loggedOut", oauthSession: null });
  await render();

  expect(container.textContent).toContain("log-in-to-manage-privacy");
  expect(container.querySelectorAll('[role="switch"]')).toHaveLength(0);
});
