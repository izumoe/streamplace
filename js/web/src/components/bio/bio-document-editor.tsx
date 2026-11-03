import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  Extension,
  Mark,
  Node,
  type ChainedCommands,
  type Editor,
  type JSONContent,
} from "@tiptap/core";
import Placeholder from "@tiptap/extension-placeholder";
import { NodeSelection, TextSelection } from "@tiptap/pm/state";
import {
  EditorContent,
  NodeViewWrapper,
  ReactNodeViewRenderer,
  useEditor,
  useEditorState,
  type NodeViewProps,
} from "@tiptap/react";
import { BubbleMenu } from "@tiptap/react/menus";
import StarterKit from "@tiptap/starter-kit";
import {
  ArrowDown,
  ArrowUp,
  Bold,
  Code,
  Italic,
  Strikethrough,
} from "lucide-react";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { place } from "streamplace";

type Entry = place.stream.bio.layouts.panels.BlockEntry;
export type BioInsertKind =
  | "Image"
  | "Link"
  | "Schedule"
  | "Social links"
  | "Embed"
  | "Bluesky post"
  | "Livestream";
type RenderBlock = (
  entry: Entry,
  update: (entry: Entry) => void,
  remove: () => void,
) => ReactNode;

export const BioMetadata = Extension.create({
  name: "bioMetadata",
  addGlobalAttributes() {
    return [
      {
        types: [
          "paragraph",
          "heading",
          "blockquote",
          "bulletList",
          "orderedList",
          "horizontalRule",
        ],
        attributes: { bioEntry: { default: null, rendered: false } },
      },
      {
        types: ["paragraph"],
        attributes: { bioRichText: { default: null, rendered: false } },
      },
      {
        types: ["listItem"],
        attributes: { bioListItem: { default: null, rendered: false } },
      },
      {
        types: [
          "bold",
          "italic",
          "strike",
          "code",
          "underline",
          "link",
          "bioFacet",
        ],
        attributes: { bioAnnotations: { default: null, rendered: false } },
      },
    ];
  },
});

export const BioFacet = Mark.create({
  name: "bioFacet",
  addAttributes() {
    return { features: { default: [], rendered: false } };
  },
  renderHTML({ mark }) {
    const highlighted = mark.attrs.features.some(
      (feature: { $type?: string }) =>
        feature.$type === "place.stream.bio.richtextFacet#highlight",
    );
    return [
      "span",
      { class: highlighted ? "bg-primary/10" : "text-inherit" },
      0,
    ];
  },
});

function AtomicBlock({
  node,
  updateAttributes,
  deleteNode,
  extension,
}: NodeViewProps) {
  return (
    <NodeViewWrapper contentEditable={false} className="my-4" data-bio-block>
      {extension.options.renderBlock(
        node.attrs.entry,
        (entry: Entry) => updateAttributes({ entry }),
        deleteNode,
      )}
    </NodeViewWrapper>
  );
}

export const BioBlock = Node.create<{ renderBlock: RenderBlock }>({
  name: "bioBlock",
  group: "block",
  atom: true,
  addAttributes() {
    return { entry: { default: null, rendered: false } };
  },
  renderHTML() {
    return ["div", { "data-bio-block": "" }];
  },
  addNodeView() {
    return ReactNodeViewRenderer(AtomicBlock);
  },
});

const FORMATTING_MENU_OPTIONS = {
  strategy: "fixed",
  placement: "top",
} as const;

export function moveSelectedBlock(editor: Editor, direction: -1 | 1) {
  const { state } = editor;
  const position = state.selection.$from.depth
    ? state.selection.$from.before(1)
    : state.selection.from;
  let previous = 0;
  let next = 0;
  state.doc.forEach((node, offset) => {
    if (offset < position) previous = offset;
    if (offset === position) next = offset + node.nodeSize;
  });
  const node = state.doc.nodeAt(position);
  const neighbor =
    direction === -1 ? state.doc.nodeAt(previous) : state.doc.nodeAt(next);
  if (!node || !neighbor || (direction === -1 && position === 0)) return;
  const target = direction === -1 ? previous : position + neighbor.nodeSize;
  const tr = state.tr
    .delete(position, position + node.nodeSize)
    .insert(target, node);
  tr.setSelection(TextSelection.near(tr.doc.resolve(target + 1)));
  editor.view.dispatch(tr);
  editor.commands.focus();
}

export function BioDocumentEditor({
  initialContent,
  label,
  disabled,
  richTextOnly = false,
  onChange,
  onError,
  onInsert,
  renderBlock,
}: {
  initialContent: () => JSONContent;
  label: string;
  disabled: boolean;
  richTextOnly?: boolean;
  onChange: (content: JSONContent) => void;
  onError: (error: string | null) => void;
  onInsert?: (kind: BioInsertKind, editor: Editor) => void;
  renderBlock?: RenderBlock;
}) {
  const { t } = useTranslation("settings");
  const [content] = useState(initialContent);
  const [insertOpen, setInsertOpen] = useState(false);
  const formattingMenu = useRef<HTMLDivElement>(null);
  const callbacks = useRef({ onChange, onError, onInsert, renderBlock });
  callbacks.current = { onChange, onError, onInsert, renderBlock };
  const editor = useEditor(
    {
      extensions: [
        StarterKit.configure({
          codeBlock: false,
          heading: richTextOnly ? false : { levels: [1, 2, 3] },
          blockquote: richTextOnly ? false : {},
          bulletList: richTextOnly ? false : {},
          orderedList: richTextOnly ? false : {},
          horizontalRule: richTextOnly ? false : {},
          trailingNode: false,
          link: {
            openOnClick: false,
            protocols: ["mailto"],
            HTMLAttributes: { class: "text-primary underline" },
          },
        }),
        BioMetadata,
        BioFacet,
        ...(!richTextOnly
          ? [
              BioBlock.configure({
                renderBlock: (entry, update, remove) =>
                  callbacks.current.renderBlock?.(entry, update, remove),
              }),
            ]
          : []),
        Placeholder.configure({
          placeholder: richTextOnly
            ? t("bio-about-placeholder", "Tell viewers about yourself…")
            : t(
                "bio-content-placeholder",
                "Write something, or type / to add a block…",
              ),
        }),
      ],
      content,
      editorProps: {
        attributes: {
          role: "textbox",
          "aria-label": label,
          "aria-multiline": "true",
          class:
            "relative min-h-24 py-4 pr-4 leading-relaxed whitespace-pre-wrap break-words outline-none focus-visible:ring-2 focus-visible:ring-ring [&_p]:my-2 [&_h1]:my-4 [&_h1]:text-2xl [&_h1]:font-semibold [&_h2]:my-4 [&_h2]:text-xl [&_h2]:font-semibold [&_h3]:my-3 [&_h3]:text-lg [&_h3]:font-semibold [&_ul]:list-disc [&_ul]:pl-6 [&_ol]:list-decimal [&_ol]:pl-6 [&_blockquote]:border-l [&_blockquote]:pl-4 [&_blockquote]:italic [&_hr]:my-4 [&_hr]:border-border [&_.is-editor-empty]:before:float-left [&_.is-editor-empty]:before:h-0 [&_.is-editor-empty]:before:text-muted-foreground [&_.is-editor-empty]:before:content-[attr(data-placeholder)] " +
            "pl-4",
        },
        handleKeyDown: (view, event) => {
          if (
            event.altKey &&
            event.key === "F10" &&
            formattingMenu.current?.isConnected
          ) {
            formattingMenu.current.querySelector("button")?.focus();
            return true;
          }
          if (
            !richTextOnly &&
            event.key === "/" &&
            view.state.selection.$from.parent.textContent === ""
          ) {
            setInsertOpen(true);
            return true;
          }
          return false;
        },
      },
      onUpdate: ({ editor }) => {
        try {
          callbacks.current.onChange(editor.getJSON());
          callbacks.current.onError(null);
        } catch (error) {
          callbacks.current.onError(
            error instanceof Error ? error.message : String(error),
          );
        }
      },
    },
    [],
  );
  useEffect(() => {
    editor?.setEditable(!disabled, false);
    if (disabled) {
      setInsertOpen(false);
    }
  }, [editor, disabled]);
  const active = useEditorState({
    editor,
    selector: ({ editor }) => ({
      bold: editor?.isActive("bold"),
      italic: editor?.isActive("italic"),
      strike: editor?.isActive("strike"),
      code: editor?.isActive("code"),
      textSelected:
        !!editor &&
        editor.state.selection instanceof TextSelection &&
        !editor.state.selection.empty &&
        !!editor.state.doc.textBetween(
          editor.state.selection.from,
          editor.state.selection.to,
        ).length,
    }),
  });
  if (!editor) return null;

  return (
    <div
      className="bg-background rounded-lg border"
      aria-label={`${label} editor`}
    >
      {!disabled && active?.textSelected && (
        <BubbleMenu
          editor={editor}
          ref={formattingMenu}
          updateDelay={0}
          options={FORMATTING_MENU_OPTIONS}
          className="bg-popover text-popover-foreground z-50 flex gap-1 rounded-lg border p-1 shadow-sm"
        >
          <div
            className="flex gap-1"
            role="toolbar"
            aria-label={`${label} formatting`}
            onKeyDown={(event) => {
              if (event.key === "Escape") {
                event.preventDefault();
                editor.view.focus();
              }
            }}
          >
            {[
              {
                name: "Bold",
                Icon: Bold,
                active: active?.bold,
                run: () => editor.chain().focus().toggleBold().run(),
              },
              {
                name: "Italic",
                Icon: Italic,
                active: active?.italic,
                run: () => editor.chain().focus().toggleItalic().run(),
              },
              {
                name: "Strikethrough",
                Icon: Strikethrough,
                active: active?.strike,
                run: () => editor.chain().focus().toggleStrike().run(),
              },
              {
                name: "Code",
                Icon: Code,
                active: active?.code,
                run: () => editor.chain().focus().toggleCode().run(),
              },
            ].map(({ name, Icon, active, run }) => (
              <Button
                key={name}
                type="button"
                variant="ghost"
                size="icon-touch"
                disabled={disabled}
                aria-label={t(`bio-format-${name.toLowerCase()}`, name)}
                aria-pressed={!!active}
                onClick={run}
              >
                <Icon />
              </Button>
            ))}
          </div>
        </BubbleMenu>
      )}
      {!richTextOnly && (
        <DropdownMenu
          modal={false}
          open={insertOpen && !disabled}
          onOpenChange={(open) => {
            setInsertOpen(open && !disabled);
          }}
        >
          <DropdownMenuTrigger
            render={
              <Button type="button" variant="ghost" disabled={disabled} />
            }
          >
            Add content or move selected block
          </DropdownMenuTrigger>
          <DropdownMenuContent className="w-48" aria-label="Insert block">
            {[
              {
                name: "Text",
                run: (chain: ChainedCommands) => chain.setParagraph().run(),
              },
              {
                name: "Heading",
                run: (chain: ChainedCommands) =>
                  chain.setHeading({ level: 2 }).run(),
              },
              {
                name: "Bullet list",
                run: (chain: ChainedCommands) => chain.toggleBulletList().run(),
              },
              {
                name: "Numbered list",
                run: (chain: ChainedCommands) =>
                  chain.toggleOrderedList().run(),
              },
              {
                name: "Quote",
                run: (chain: ChainedCommands) =>
                  chain.setParagraph().toggleBlockquote().run(),
              },
              {
                name: "Divider",
                run: (chain: ChainedCommands) =>
                  chain.setHorizontalRule().run(),
              },
            ].map(({ name, run }) => (
              <DropdownMenuItem
                key={name}
                onClick={() => {
                  const selection = editor.state.selection;
                  const chain = editor.chain().focus();
                  if (selection instanceof NodeSelection) {
                    chain
                      .insertContentAt(selection.to, { type: "paragraph" })
                      .setTextSelection(selection.to + 1);
                  }
                  run(chain);
                }}
              >
                {t(
                  `bio-insert-${name.toLowerCase().replaceAll(" ", "-")}`,
                  name,
                )}
              </DropdownMenuItem>
            ))}
            {(
              [
                "Image",
                "Link",
                "Schedule",
                "Social links",
                "Embed",
                "Bluesky post",
                "Livestream",
              ] as const
            ).map((kind) => (
              <DropdownMenuItem
                key={kind}
                onClick={() => callbacks.current.onInsert?.(kind, editor)}
              >
                {t(
                  `bio-insert-${kind.toLowerCase().replaceAll(" ", "-")}`,
                  kind,
                )}
              </DropdownMenuItem>
            ))}
            <DropdownMenuSeparator />
            <DropdownMenuItem onClick={() => moveSelectedBlock(editor, -1)}>
              <ArrowUp />
              {t("bio-move-block-up", "Move block up")}
            </DropdownMenuItem>
            <DropdownMenuItem onClick={() => moveSelectedBlock(editor, 1)}>
              <ArrowDown />
              {t("bio-move-block-down", "Move block down")}
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      )}
      <EditorContent editor={editor} />
    </div>
  );
}
