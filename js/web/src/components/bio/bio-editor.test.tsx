// @vitest-environment jsdom
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from "@tanstack/react-router";
import type { Editor } from "@tiptap/core";
import { createInstance } from "i18next";
import { act, type ComponentProps } from "react";
import { createRoot } from "react-dom/client";
import { I18nextProvider, initReactI18next } from "react-i18next";
import { afterAll, expect, it, vi } from "vitest";
import { BioEditor } from "./bio-editor";

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

async function mountEditor(onSave: ComponentProps<typeof BioEditor>["onSave"]) {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      disconnect() {}
    },
  );
  vi.stubGlobal("matchMedia", () => ({
    matches: false,
    addEventListener() {},
    removeEventListener() {},
  }));
  const scrollTo = vi.spyOn(window, "scrollTo").mockImplementation(() => {});
  const i18n = createInstance();
  await i18n.use(initReactI18next).init({
    lng: "en",
    resources: { en: { settings: {} } },
    defaultNS: "settings",
  });
  const route = createRootRoute({
    component: () => (
      <BioEditor
        did="did:plc:alice"
        initialBio={{
          $type: "place.stream.bio.page",
          createdAt: "2026-01-01T00:00:00Z",
          layout: {
            $type: "place.stream.bio.layouts.panels",
            panels: [
              {
                blocks: [
                  {
                    block: {
                      $type: "place.stream.bio.blocks.link",
                      url: "https://example.com",
                      text: "Original link",
                    },
                  },
                ],
              },
            ],
          },
        }}
        onSave={onSave}
        onUploadImage={async () => {
          throw new Error("unexpected upload");
        }}
        onResolveRecord={async () => {
          throw new Error("unexpected lookup");
        }}
      />
    ),
  });
  const router = createRouter({
    routeTree: route,
    history: createMemoryHistory(),
  });
  await router.load();
  const container = document.createElement("div");
  document.body.append(container);
  const root = createRoot(container);
  const button = (label: string) =>
    Array.from(document.querySelectorAll<HTMLButtonElement>("button")).find(
      (entry) =>
        entry.getAttribute("aria-label") === label ||
        entry.textContent === label,
    )!;
  await act(async () =>
    root.render(
      <I18nextProvider i18n={i18n}>
        <RouterProvider router={router} />
      </I18nextProvider>,
    ),
  );
  return {
    container,
    button,
    async dispose() {
      await act(async () => root.unmount());
      container.remove();
      vi.unstubAllGlobals();
      scrollTo.mockRestore();
    },
  };
}

it("passes responsive motion styles through the custom editor card", async () => {
  const view = await mountEditor(async () => {});
  try {
    const card = view.container.querySelector("[data-bio-card]")!;
    expect(card.classList.contains("motion-safe:transition-[left,top]")).toBe(
      true,
    );
    expect(card.classList.contains("motion-reduce:transition-none")).toBe(true);
  } finally {
    await view.dispose();
  }
});

it("locks mounted atomic controls and their open dialog until a save completes", async () => {
  let finishSave!: () => void;
  const saving = new Promise<void>((resolve) => {
    finishSave = resolve;
  });
  const onSave = vi.fn(() => saving);
  const view = await mountEditor(onSave);
  const { container, button } = view;
  try {
    await act(async () => button("Edit block").click());
    const field = document.querySelector<HTMLInputElement>(
      '[role="dialog"] input',
    );
    expect(field).not.toBeNull();
    const about = container.querySelector<HTMLElement>(
      '[role="textbox"][aria-label="About you"]',
    ) as HTMLElement & { editor: Editor };
    await act(async () => {
      about.editor.commands.insertContent("A changed bio");
    });
    await act(async () => button("Save bio").click());
    expect(onSave).toHaveBeenCalledTimes(1);
    expect(button("Edit block").matches(":disabled")).toBe(true);
    expect(button("Remove block").matches(":disabled")).toBe(true);
    expect(field!.matches(":disabled")).toBe(true);
    const panel = container.querySelector<HTMLElement>(
      '[role="textbox"][aria-label="Bio content"]',
    ) as HTMLElement & { editor: Editor };
    const submittedPanel = panel.editor.getJSON();
    await act(async () => {
      // A disabled field can still receive a programmatic change event.
      Object.getOwnPropertyDescriptor(
        HTMLInputElement.prototype,
        "value",
      )!.set!.call(field, "https://late-edit.example");
      field!.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(panel.editor.getJSON()).toEqual(submittedPanel);
    await act(async () => {
      button("Remove block").click();
      finishSave();
      await saving;
    });
    expect(container.querySelector("[data-bio-block]")).not.toBeNull();
    expect(button("Edit block").matches(":disabled")).toBe(false);
    expect(field!.matches(":disabled")).toBe(false);
    expect(button("Save bio").matches(":disabled")).toBe(true);
    await act(async () => {
      Object.getOwnPropertyDescriptor(
        HTMLInputElement.prototype,
        "value",
      )!.set!.call(field, "https://after-save.example");
      field!.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(JSON.stringify(panel.editor.getJSON())).toContain(
      "https://after-save.example",
    );
    expect(button("Save bio").matches(":disabled")).toBe(false);
  } finally {
    finishSave();
    await view.dispose();
  }
});
