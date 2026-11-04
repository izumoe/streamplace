// @vitest-environment jsdom
import { Editor, type JSONContent } from "@tiptap/core";
import { NodeSelection } from "@tiptap/pm/state";
import StarterKit from "@tiptap/starter-kit";
import { createInstance } from "i18next";
import { act, type ComponentProps } from "react";
import { createRoot } from "react-dom/client";
import { I18nextProvider, initReactI18next } from "react-i18next";
import { describe, expect, it, vi } from "vitest";
import {
  BioDocumentEditor,
  BioFacet,
  BioMetadata,
  moveSelectedBlock,
} from "./bio-document-editor";

const extensions = [
  StarterKit.configure({
    trailingNode: false,
    codeBlock: false,
    heading: { levels: [1, 2, 3] },
  }),
  BioMetadata,
  BioFacet,
];

const heading: JSONContent = {
  type: "heading",
  attrs: {
    level: 1,
    bioEntry: {
      alignment: "right",
      block: {
        $type: "place.stream.bio.blocks.header",
        plaintext: "heading",
        level: 1,
      },
    },
  },
  content: [{ type: "text", text: "heading" }],
};
const paragraph: JSONContent = {
  type: "paragraph",
  content: [{ type: "text", text: "body", marks: [{ type: "bold" }] }],
};

describe("bio document reordering", () => {
  it("moves the selected heading below a paragraph without losing marks or metadata", () => {
    const editor = new Editor({
      extensions,
      content: { type: "doc", content: [heading, paragraph] },
    });
    try {
      editor.commands.setTextSelection(1);
      const original = editor.getJSON().content!;
      moveSelectedBlock(editor, 1);
      expect(editor.getJSON().content).toEqual([original[1], original[0]]);
      moveSelectedBlock(editor, -1);
      expect(editor.getJSON().content).toEqual(original);
    } finally {
      editor.destroy();
    }
  });

  it("keeps the document at its boundaries", () => {
    const editor = new Editor({
      extensions,
      content: { type: "doc", content: [heading, paragraph] },
    });
    try {
      const original = editor.getJSON();
      editor.commands.setTextSelection(1);
      moveSelectedBlock(editor, -1);
      editor.commands.setTextSelection(editor.state.doc.content.size - 1);
      moveSelectedBlock(editor, 1);
      expect(editor.getJSON()).toEqual(original);
    } finally {
      editor.destroy();
    }
  });
});

async function mountBioEditor(
  props: Partial<ComponentProps<typeof BioDocumentEditor>> = {},
) {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  const i18n = createInstance();
  await i18n.use(initReactI18next).init({
    lng: "en",
    resources: { en: { settings: {} } },
    defaultNS: "settings",
  });
  // jsdom has no layout; editor selection and floating menus need these DOM APIs.
  const layout = [
    {
      target: window,
      key: "scrollBy",
      value: () => {},
    },
    {
      target: document,
      key: "elementFromPoint",
      value: () => null,
    },
    {
      target: Range.prototype,
      key: "getClientRects",
      value: () => [new DOMRect(0, 0, 200, 24)],
    },
    {
      target: Range.prototype,
      key: "getBoundingClientRect",
      value: () => new DOMRect(0, 0, 200, 24),
    },
  ].map((api) => ({
    ...api,
    descriptor: Object.getOwnPropertyDescriptor(api.target, api.key),
  }));
  for (const { target, key, value } of layout)
    Object.defineProperty(target, key, { configurable: true, value });
  const container = document.createElement("div");
  document.body.append(container);
  const root = createRoot(container);
  const changes: JSONContent[] = [];
  const errors: (string | null)[] = [];
  const render = async (disabled: boolean) => {
    await act(async () =>
      root.render(
        <I18nextProvider i18n={i18n}>
          <BioDocumentEditor
            initialContent={() => ({ type: "doc", content: [paragraph] })}
            label="Bio content"
            onChange={(content) => changes.push(content)}
            onError={(error) => errors.push(error)}
            {...props}
            disabled={disabled}
          />
        </I18nextProvider>,
      ),
    );
  };
  await render(false);
  const textbox = container.querySelector('[role="textbox"]') as HTMLElement & {
    editor: Editor;
  };
  return {
    container,
    textbox,
    editor: textbox.editor,
    changes,
    errors,
    render,
    async dispose() {
      await act(async () => root.unmount());
      container.remove();
      for (const { target, key, descriptor } of layout) {
        if (descriptor) Object.defineProperty(target, key, descriptor);
        else Reflect.deleteProperty(target, key);
      }
      vi.unstubAllGlobals();
    },
  };
}

describe("bio contextual controls", () => {
  it("inserts a supported quote from a slash command in an empty heading", async () => {
    const view = await mountBioEditor({
      initialContent: () => ({
        type: "doc",
        content: [{ type: "heading", attrs: { level: 2 } }],
      }),
    });
    try {
      await act(async () => {
        view.editor.view.focus();
        view.editor.commands.setTextSelection(1);
        view.textbox.dispatchEvent(
          new KeyboardEvent("keydown", {
            key: "/",
            bubbles: true,
            cancelable: true,
          }),
        );
      });
      expect(view.changes).toEqual([]);
      const quote = Array.from(
        document.querySelectorAll('[role="menuitem"]'),
      ).find((item) => item.textContent === "Quote") as HTMLElement;
      await act(async () => quote.click());
      const block = view.editor.getJSON().content![0];
      expect(block.type).toBe("blockquote");
      expect(block.content![0].type).toBe("paragraph");
      expect(view.changes).toHaveLength(1);
    } finally {
      await view.dispose();
    }
  });

  it.each([
    [heading, "Heading", "heading"],
    [paragraph, "Text", "paragraph"],
    [
      {
        type: "bulletList",
        attrs: { bioEntry: { alignment: "right", imported: true } },
        content: [{ type: "listItem", content: [paragraph] }],
      },
      "Quote",
      "blockquote",
    ],
    [
      {
        type: "blockquote",
        attrs: { bioEntry: { alignment: "right", imported: true } },
        content: [paragraph],
      },
      "Numbered list",
      "orderedList",
    ],
    [
      {
        type: "bioBlock",
        attrs: { entry: { block: { $type: "future.block" } } },
      },
      "Bullet list",
      "bulletList",
    ],
    [heading, "Divider", "horizontalRule"],
  ] as const)(
    "inserts %s after a selected block without changing the original",
    async (source, name, type) => {
      const view = await mountBioEditor({
        initialContent: () => ({
          type: "doc",
          content: [source as JSONContent, paragraph],
        }),
        renderBlock: () => <span>Imported block</span>,
      });
      try {
        const original = view.editor.getJSON();
        await act(async () => {
          view.editor.view.dispatch(
            view.editor.state.tr.setSelection(
              NodeSelection.create(view.editor.state.doc, 0),
            ),
          );
          (
            view.container.querySelector(
              '[data-slot="dropdown-menu-trigger"]',
            ) as HTMLButtonElement
          ).click();
        });
        expect(view.changes).toEqual([]);
        const item = Array.from(
          document.querySelectorAll('[role="menuitem"]'),
        ).find((item) => item.textContent === name) as HTMLElement;
        await act(async () => item.click());
        const blocks = view.editor.getJSON().content!;
        expect(blocks).toHaveLength(3);
        expect(blocks[0]).toEqual(original.content![0]);
        expect(blocks[1].type).toBe(type);
        expect(blocks[2]).toEqual(original.content![1]);
        expect(view.changes).toHaveLength(1);
        await act(async () => {
          expect(view.editor.commands.undo()).toBe(true);
        });
        expect(view.editor.getJSON()).toEqual(original);
      } finally {
        await view.dispose();
      }
    },
  );

  it("shows formatting only for selected text and keeps that selection when applying marks", async () => {
    const view = await mountBioEditor();
    try {
      expect(document.querySelector('[role="toolbar"]')).toBeNull();
      await act(async () => {
        view.editor.view.focus();
        view.editor.commands.setTextSelection({ from: 1, to: 5 });
      });
      const toolbar = document.querySelector('[role="toolbar"]');
      expect(toolbar?.getAttribute("aria-label")).toBe(
        "Bio content formatting",
      );
      await act(async () => {
        view.textbox.dispatchEvent(
          new KeyboardEvent("keydown", {
            bubbles: true,
            key: "F10",
            altKey: true,
          }),
        );
      });
      expect(document.activeElement?.getAttribute("aria-label")).toBe("Bold");
      await act(async () => {
        document.activeElement!.dispatchEvent(
          new KeyboardEvent("keydown", { bubbles: true, key: "Escape" }),
        );
      });
      expect(document.activeElement).toBe(view.textbox);
      const italic = toolbar?.querySelector('[aria-label="Italic"]');
      expect(italic).not.toBeNull();
      await act(async () => {
        const mousedown = new MouseEvent("mousedown", {
          bubbles: true,
          cancelable: true,
        });
        italic!.dispatchEvent(mousedown);
        expect(mousedown.defaultPrevented).toBe(false);
        (italic as HTMLButtonElement).focus();
        (italic as HTMLButtonElement).click();
      });
      expect(view.editor.state.selection.from).toBe(1);
      expect(view.editor.state.selection.to).toBe(5);
      expect(view.editor.getJSON().content![0].content![0].marks).toEqual([
        { type: "bold", attrs: { bioAnnotations: null } },
        { type: "italic", attrs: { bioAnnotations: null } },
      ]);
      await vi.waitFor(() => expect(document.activeElement).toBe(view.textbox));
      const outsideButton = document.createElement("button");
      document.body.append(outsideButton);
      try {
        await act(async () => outsideButton.focus());
        expect(document.querySelector('[role="toolbar"]')).toBeNull();
      } finally {
        outsideButton.remove();
      }
      await view.render(true);
      expect(document.querySelector('[role="toolbar"]')).toBeNull();
      await view.render(false);
      await act(async () => view.editor.commands.setTextSelection(1));
      expect(document.querySelector('[role="toolbar"]')).toBeNull();
    } finally {
      await view.dispose();
    }
  });

  it("uses the same selection formatting for About you without block actions", async () => {
    const view = await mountBioEditor({ richTextOnly: true });
    try {
      expect(document.querySelector('[role="toolbar"]')).toBeNull();
      expect(
        view.container.querySelector('[aria-label="Drag block"]'),
      ).toBeNull();
      await act(async () => {
        view.editor.view.focus();
        view.editor.commands.setTextSelection({ from: 1, to: 5 });
      });
      expect(document.querySelector('[role="toolbar"]')).not.toBeNull();
      await act(async () => view.editor.commands.setTextSelection(2));
      expect(document.querySelector('[role="toolbar"]')).toBeNull();
    } finally {
      await view.dispose();
    }
  });
});

describe("bio document editability", () => {
  it("keeps content and change state intact while saving or uploading disables the editor", async () => {
    const view = await mountBioEditor();
    try {
      expect(view.textbox.textContent).toBe("body");
      expect(view.changes).toEqual([]);
      expect(view.errors).toEqual([]);
      await view.render(true);
      expect(view.textbox.getAttribute("contenteditable")).toBe("false");
      await view.render(false);
      expect(view.textbox.getAttribute("contenteditable")).toBe("true");
      expect(view.textbox.querySelector("strong")?.textContent).toBe("body");
      expect(view.changes).toEqual([]);
      expect(view.errors).toEqual([]);
    } finally {
      await view.dispose();
    }
  });
});
