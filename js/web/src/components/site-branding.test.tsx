import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from "@tanstack/react-router";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, beforeAll, expect, it, vi } from "vitest";
import { SessionProvider } from "../lib/session";
import { useStore } from "../lib/store";
import SidebarComponent from "./sidebar";
import { SidebarProvider } from "./ui/sidebar";

const { warnings } = vi.hoisted(() => ({
  warnings: vi.spyOn(console, "warn").mockImplementation(() => {}),
}));
vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));
const initial = useStore.getState();
let root: ReturnType<typeof createRoot> | undefined;
let container: HTMLDivElement;
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
afterEach(async () => {
  if (root) await act(async () => root!.unmount());
  container?.remove();
  useStore.setState(initial, true);
  vi.unstubAllGlobals();
});
it("shows saved social and legal links to public visitors without rendering unsafe URLs", async () => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  vi.stubGlobal("scrollTo", () => {});
  vi.stubGlobal("matchMedia", () => ({
    matches: false,
    addEventListener() {},
    removeEventListener() {},
  }));
  useStore.setState({
    authStatus: "loggedOut",
    branding: {
      socialLinks: {
        key: "socialLinks",
        mimeType: "text/plain",
        data: '[{"label":"Community","url":"https://community.example"}]',
      },
      legalLinks: {
        key: "legalLinks",
        mimeType: "text/plain",
        data: '[{"text":"Privacy","url":"https://node.example/privacy"},{"text":"Unsafe","url":"javascript:alert(1)"}]',
      },
    },
  });
  const route = createRootRoute({
    component: () => (
      <SessionProvider>
        <SidebarProvider>
          <SidebarComponent />
        </SidebarProvider>
      </SessionProvider>
    ),
  });
  const router = createRouter({
    routeTree: route,
    scrollRestoration: false,
    history: createMemoryHistory({ initialEntries: ["/"] }),
  });
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => {
    await router.load();
    root!.render(<RouterProvider router={router} />);
  });
  expect(
    container.querySelector('a[href="https://community.example"]')?.textContent,
  ).toBe("Community");
  expect(
    container.querySelector('a[href="https://node.example/privacy"]')
      ?.textContent,
  ).toBe("Privacy");
  expect(container.querySelector('a[href^="javascript:"]')).toBeNull();
});
