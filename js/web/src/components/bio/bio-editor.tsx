import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import type { ResolveBioRecord } from "@/lib/bio-record-reference";
import { DragDropProvider, DragOverlay } from "@dnd-kit/react";
import { isSortable, useSortable } from "@dnd-kit/react/sortable";
import {
  bioRichTextToDocument,
  documentToBioRichText,
  documentToPanelBlocks,
  getBioLinkUrl,
  moveBioItem,
  panelBlocksToDocument,
  prepareBioForSave,
  setBioPlaintext,
} from "@streamplace/core";
import { useBlocker } from "@tanstack/react-router";
import type { Editor } from "@tiptap/core";
import { NodeSelection } from "@tiptap/pm/state";
import {
  ArrowDown,
  ArrowUp,
  Eye,
  GripVertical,
  Pencil,
  Plus,
  Trash2,
} from "lucide-react";
import {
  createContext,
  useContext,
  useEffect,
  useRef,
  useState,
  type CSSProperties,
  type ReactNode,
} from "react";
import { useTranslation } from "react-i18next";
import { place } from "streamplace";
import { BioDocumentEditor, type BioInsertKind } from "./bio-document-editor";
import { BioPanelsLayout } from "./bio-panels-layout";
import { BioRecordReference } from "./bio-record-reference";
import { BioBlockView, BioViewer } from "./bio-viewer";

type Bio = place.stream.bio.page.Main;
type Panel = place.stream.bio.layouts.panels.Panel;
type Block = place.stream.bio.layouts.panels.BlockEntry["block"];
type Social = place.stream.bio.defs.Social;
type ImageBlock = place.stream.bio.blocks.image.Main;

const BioEditorBusyContext = createContext(false);

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <label className="block space-y-2 text-sm">
      <span className="font-medium">{label}</span>
      {children}
    </label>
  );
}

function OrderButtons({
  index,
  count,
  onMove,
  onRemove,
  kind,
}: {
  index: number;
  count: number;
  onMove: (to: number) => void;
  onRemove: () => void;
  kind: string;
}) {
  const { t } = useTranslation("settings");
  return (
    <div className="flex gap-2">
      <Button
        type="button"
        variant="ghost"
        size="icon-touch"
        aria-label={t("bio-move-up", { defaultValue: `Move ${kind} up`, kind })}
        disabled={index === 0}
        onClick={() => onMove(index - 1)}
      >
        <ArrowUp />
      </Button>
      <Button
        type="button"
        variant="ghost"
        size="icon-touch"
        aria-label={t("bio-move-down", {
          defaultValue: `Move ${kind} down`,
          kind,
        })}
        disabled={index === count - 1}
        onClick={() => onMove(index + 1)}
      >
        <ArrowDown />
      </Button>
      <Button
        type="button"
        variant="destructive"
        size="icon-touch"
        aria-label={t("bio-remove", { defaultValue: `Remove ${kind}`, kind })}
        onClick={onRemove}
      >
        <Trash2 />
      </Button>
    </div>
  );
}

function BioPanelEditor({
  id,
  index,
  count,
  disabled,
  style,
  className,
  onMove,
  onRemove,
  children,
}: {
  id: string;
  index: number;
  count: number;
  disabled: boolean;
  style?: CSSProperties;
  className?: string;
  onMove: (to: number) => void;
  onRemove: () => void;
  children: ReactNode;
}) {
  const { t } = useTranslation("settings");
  const { ref, handleRef, isDragSource, isDropTarget } = useSortable({
    id,
    index,
    disabled,
    transition: null,
  });
  return (
    <Card
      ref={ref}
      data-bio-card={id}
      data-bio-drop-target={(isDropTarget && !isDragSource) || undefined}
      role="region"
      aria-label={`Card ${index + 1}`}
      className={`mb-4 w-(--bio-panel-width) max-w-full min-w-0 gap-0 py-0 ${isDragSource ? "opacity-50" : ""} ${isDropTarget && !isDragSource ? "ring-primary ring-2" : ""} ${className ?? ""}`}
      style={style}
    >
      <div className="relative flex items-center gap-2 border-b px-3 py-2">
        {isDropTarget && !isDragSource && (
          <p
            className="bg-primary text-primary-foreground absolute inset-x-0 top-0 rounded-t-lg px-3 text-xs"
            role="status"
          >
            Drop here to move to position {index + 1}
          </p>
        )}
        <Button
          ref={handleRef}
          type="button"
          variant="ghost"
          size="icon-touch"
          disabled={disabled}
          className="cursor-grab touch-none active:cursor-grabbing"
          aria-label={t("bio-drag-card", {
            defaultValue: `Drag card ${index + 1}`,
            number: index + 1,
          })}
          title={t(
            "bio-drag-card-help",
            "Drag to reorder, or press Space and use the arrow keys.",
          )}
        >
          <GripVertical />
        </Button>
        <h4 className="text-sm font-medium">
          {t("bio-card-number", {
            defaultValue: `Card ${index + 1}`,
            number: index + 1,
          })}
        </h4>
        <fieldset disabled={disabled} className="ml-auto">
          <OrderButtons
            kind="card"
            index={index}
            count={count}
            onMove={onMove}
            onRemove={onRemove}
          />
        </fieldset>
      </div>
      <div className="[&>div]:rounded-none [&>div]:border-0 [&>div]:bg-transparent">
        {children}
      </div>
    </Card>
  );
}

function SocialFields({
  socials,
  onChange,
}: {
  socials: Social[];
  onChange: (socials: Social[]) => void;
}) {
  const { t } = useTranslation("settings");
  return (
    <div className="space-y-4">
      {socials.map((social, i) => (
        <div key={i} className="space-y-3 rounded-lg border p-4">
          <Field label={t("bio-social-url", "Social URL")}>
            <Input
              value={social.url}
              onChange={(e) =>
                onChange(
                  socials.map((s, j) =>
                    j === i
                      ? { ...s, url: e.target.value as Social["url"] }
                      : s,
                  ),
                )
              }
            />
          </Field>
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label={t("bio-platform", "Platform")}>
              <Input
                value={social.platform}
                onChange={(e) =>
                  onChange(
                    socials.map((s, j) =>
                      j === i ? { ...s, platform: e.target.value } : s,
                    ),
                  )
                }
              />
            </Field>
            <Field label={t("bio-handle", "Display name or handle")}>
              <Input
                value={social.handle ?? ""}
                onChange={(e) =>
                  onChange(
                    socials.map((s, j) =>
                      j === i ? { ...s, handle: e.target.value } : s,
                    ),
                  )
                }
              />
            </Field>
          </div>
          <Button
            type="button"
            variant="destructive"
            onClick={() => onChange(socials.filter((_, j) => j !== i))}
          >
            {t("bio-remove-social", "Remove social link")}
          </Button>
        </div>
      ))}
      <Button
        type="button"
        variant="outline"
        disabled={socials.length >= 16}
        onClick={() =>
          onChange([
            ...socials,
            { platform: "website", url: "" as Social["url"] },
          ])
        }
      >
        <Plus />
        {t("bio-add-social", "Add social link")}
      </Button>
    </div>
  );
}

function BlockFields({
  block,
  onChange,
  onResolveRecord,
}: {
  block: Block;
  onChange: (block: Block) => void;
  onResolveRecord: ResolveBioRecord;
}) {
  const { t } = useTranslation("settings");
  const blocks = place.stream.bio.blocks;
  if (
    blocks.text.main.isTypeOf(block) ||
    blocks.header.main.isTypeOf(block) ||
    blocks.blockquote.main.isTypeOf(block)
  ) {
    const label = blocks.header.main.isTypeOf(block)
      ? t("bio-heading-text", "Heading text")
      : t("bio-text-content", "Text content");
    return (
      <div className="space-y-3">
        <Field label={label}>
          <Textarea
            value={block.plaintext}
            onChange={(e) => onChange(setBioPlaintext(block, e.target.value))}
          />
        </Field>
        {block.facets?.length ? (
          <p className="text-muted-foreground text-sm">
            {t(
              "bio-edit-formatting",
              "Changing this text clears its imported formatting.",
            )}
          </p>
        ) : null}
        {blocks.header.main.isTypeOf(block) && (
          <Field label={t("bio-heading-level", "Heading level")}>
            <Input
              type="number"
              min={1}
              max={3}
              value={block.level ?? 2}
              onChange={(e) =>
                onChange({ ...block, level: Number(e.target.value) })
              }
            />
          </Field>
        )}
        {blocks.text.main.isTypeOf(block) && (
          <Field label={t("bio-text-size", "Text size")}>
            <select
              className="bg-background rounded-lg border p-2"
              value={block.textSize ?? "default"}
              onChange={(e) =>
                onChange({
                  ...block,
                  textSize: e.target.value as "default" | "small" | "large",
                })
              }
            >
              <option value="default">
                {t("bio-size-default", "Default")}
              </option>
              <option value="small">{t("bio-size-small", "Small")}</option>
              <option value="large">{t("bio-size-large", "Large")}</option>
            </select>
          </Field>
        )}
      </div>
    );
  }
  if (blocks.link.main.isTypeOf(block))
    return (
      <div className="space-y-3">
        <Field label={t("bio-link-url", "Link URL")}>
          <Input
            value={block.url}
            onChange={(e) =>
              onChange({ ...block, url: e.target.value as Social["url"] })
            }
          />
        </Field>
        <Field label={t("bio-link-text", "Link label")}>
          <Input
            value={block.text ?? ""}
            onChange={(e) => onChange({ ...block, text: e.target.value })}
          />
        </Field>
        <Field label={t("bio-link-description", "Link description")}>
          <Textarea
            value={block.description ?? ""}
            onChange={(e) =>
              onChange({ ...block, description: e.target.value })
            }
          />
        </Field>
      </div>
    );
  if (blocks.image.main.isTypeOf(block))
    return (
      <div className="space-y-3">
        <Field label={t("bio-image-alt", "Image description")}>
          <Textarea
            value={block.alt ?? ""}
            onChange={(e) => onChange({ ...block, alt: e.target.value })}
          />
        </Field>
        <Field label={t("bio-image-link", "Image link (optional)")}>
          <Input
            value={block.url ?? ""}
            placeholder="https://…"
            onChange={(e) =>
              onChange({
                ...block,
                url: (e.target.value || undefined) as ImageBlock["url"],
              })
            }
          />
        </Field>
      </div>
    );
  if (blocks.divider.main.isTypeOf(block)) return null;
  if (blocks.socialLinks.main.isTypeOf(block))
    return (
      <SocialFields
        socials={block.links}
        onChange={(links) => onChange({ ...block, links })}
      />
    );
  if (
    blocks.bskyPost.main.isTypeOf(block) ||
    blocks.livestream.main.isTypeOf(block)
  ) {
    const post = blocks.bskyPost.main.isTypeOf(block);
    const ref = post
      ? block.post
      : (block as place.stream.bio.blocks.livestream.Main).livestream;
    const updateRef = (next: typeof ref) =>
      onChange(
        post ? { ...block, post: next } : { ...block, livestream: next },
      );
    return (
      <BioRecordReference
        collection={post ? "app.bsky.feed.post" : "place.stream.livestream"}
        value={ref}
        onResolveRecord={onResolveRecord}
        onChange={updateRef}
      />
    );
  }
  if (blocks.embed.main.isTypeOf(block))
    return (
      <div className="space-y-3">
        <Field label={t("bio-embed-url", "Embed URL")}>
          <Input
            value={block.src}
            onChange={(e) =>
              onChange({ ...block, src: e.target.value as typeof block.src })
            }
          />
        </Field>
        <Field label={t("bio-embed-title", "Embed title")}>
          <Input
            value={block.title ?? ""}
            onChange={(e) => onChange({ ...block, title: e.target.value })}
          />
        </Field>
        <Field label={t("bio-link-description", "Link description")}>
          <Textarea
            value={block.description ?? ""}
            onChange={(e) =>
              onChange({ ...block, description: e.target.value })
            }
          />
        </Field>
      </div>
    );
  if (
    blocks.orderedList.main.isTypeOf(block) ||
    blocks.unorderedList.main.isTypeOf(block)
  ) {
    return (
      <div className="space-y-3">
        {block.children.map((item, i) => (
          <div key={i} className="space-y-3 border-l pl-4">
            <BlockFields
              onResolveRecord={onResolveRecord}
              block={item.content}
              onChange={(content) =>
                onChange({
                  ...block,
                  children: block.children.map((entry, j) =>
                    j === i ? { ...entry, content } : entry,
                  ),
                } as Block)
              }
            />
            <OrderButtons
              kind="list item"
              index={i}
              count={block.children.length}
              onMove={(to) =>
                onChange({
                  ...block,
                  children: moveBioItem<
                    | place.stream.bio.blocks.orderedList.ListItem
                    | place.stream.bio.blocks.unorderedList.ListItem
                  >(block.children, i, to),
                } as Block)
              }
              onRemove={() =>
                onChange({
                  ...block,
                  children: block.children.filter((_, j) => j !== i),
                } as Block)
              }
            />
          </div>
        ))}
        <Button
          type="button"
          variant="outline"
          onClick={() =>
            onChange({
              ...block,
              children: [
                ...block.children,
                {
                  content: {
                    $type: "place.stream.bio.blocks.text",
                    plaintext: "",
                  },
                },
              ],
            } as Block)
          }
        >
          {t("bio-add-list-item", "Add list item")}
        </Button>
      </div>
    );
  }
  if (blocks.schedule.main.isTypeOf(block))
    return (
      <div className="space-y-3">
        <Field label={t("bio-timezone", "Timezone")}>
          <Input
            value={block.timezone}
            onChange={(e) => onChange({ ...block, timezone: e.target.value })}
          />
        </Field>
        {block.slots.map((slot, i) => (
          <div key={i} className="space-y-3 rounded-lg border p-4">
            <Field label={t("bio-schedule-day", "Day")}>
              <select
                className="bg-background rounded-lg border p-2"
                value={slot.dayOfWeek}
                onChange={(e) =>
                  onChange({
                    ...block,
                    slots: block.slots.map((s, j) =>
                      j === i
                        ? {
                            ...s,
                            dayOfWeek: e.target.value as typeof slot.dayOfWeek,
                          }
                        : s,
                    ),
                  })
                }
              >
                {["mon", "tue", "wed", "thu", "fri", "sat", "sun"].map(
                  (day) => (
                    <option key={day} value={day}>
                      {t(`bio-day-${day}`, day)}
                    </option>
                  ),
                )}
              </select>
            </Field>
            <Field label={t("bio-schedule-start", "Start time")}>
              <Input
                type="time"
                value={slot.startTime}
                onChange={(e) =>
                  onChange({
                    ...block,
                    slots: block.slots.map((s, j) =>
                      j === i ? { ...s, startTime: e.target.value } : s,
                    ),
                  })
                }
              />
            </Field>
            <Field label={t("bio-schedule-end", "End time (optional)")}>
              <Input
                type="time"
                value={slot.endTime ?? ""}
                onChange={(e) =>
                  onChange({
                    ...block,
                    slots: block.slots.map((s, j) =>
                      j === i
                        ? { ...s, endTime: e.target.value || undefined }
                        : s,
                    ),
                  })
                }
              />
            </Field>
            <Field label={t("bio-schedule-title", "Stream title")}>
              <Input
                value={slot.title ?? ""}
                onChange={(e) =>
                  onChange({
                    ...block,
                    slots: block.slots.map((s, j) =>
                      j === i ? { ...s, title: e.target.value } : s,
                    ),
                  })
                }
              />
            </Field>
            <Button
              type="button"
              variant="destructive"
              onClick={() =>
                onChange({
                  ...block,
                  slots: block.slots.filter((_, j) => j !== i),
                })
              }
            >
              {t("bio-remove-slot", "Remove time slot")}
            </Button>
          </div>
        ))}
        <Button
          type="button"
          variant="outline"
          disabled={block.slots.length >= 21}
          onClick={() =>
            onChange({
              ...block,
              slots: [...block.slots, { dayOfWeek: "mon", startTime: "18:00" }],
            })
          }
        >
          {t("bio-add-slot", "Add time slot")}
        </Button>
      </div>
    );
  return (
    <p className="text-muted-foreground text-sm">
      {t(
        "bio-unsupported-block",
        "This block is preserved. You can move or remove it.",
      )}
    </p>
  );
}

function BioAtomicBlock({
  entry,
  did,
  imagePreviews,
  onChange,
  onRemove,
  onResolveRecord,
}: {
  entry: place.stream.bio.layouts.panels.BlockEntry;
  did: string;
  imagePreviews: ReadonlyMap<string, string>;
  onChange: (entry: place.stream.bio.layouts.panels.BlockEntry) => void;
  onRemove: () => void;
  onResolveRecord: ResolveBioRecord;
}) {
  const [open, setOpen] = useState(false);
  const busy = useContext(BioEditorBusyContext);
  const { t } = useTranslation("settings");
  return (
    <div className="group relative rounded-lg border p-4">
      <div className="mb-2 flex items-center gap-1">
        <div className="ml-auto flex gap-1">
          <Button
            type="button"
            variant="ghost"
            size="icon-touch"
            aria-label="Edit block"
            disabled={busy}
            onClick={() => setOpen(true)}
          >
            <Pencil />
          </Button>
          <Button
            type="button"
            variant="ghost"
            size="icon-touch"
            aria-label="Remove block"
            disabled={busy}
            onClick={onRemove}
          >
            <Trash2 />
          </Button>
        </div>
      </div>
      <BioBlockView
        block={entry.block}
        did={did}
        imagePreviews={imagePreviews}
      />
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("bio-edit-block", "Edit block")}</DialogTitle>
          </DialogHeader>
          <fieldset disabled={busy}>
            <BlockFields
              block={entry.block}
              onResolveRecord={onResolveRecord}
              onChange={(block) => onChange({ ...entry, block })}
            />
          </fieldset>
          <Button type="button" className="mt-4" onClick={() => setOpen(false)}>
            {t("done", "Done")}
          </Button>
        </DialogContent>
      </Dialog>
    </div>
  );
}

const emptyPanels: Panel[] = [{ blocks: [] }];

export function BioEditor({
  initialBio,
  did,
  onSave,
  onUploadImage,
  onResolveRecord,
}: {
  initialBio: Bio;
  did: string;
  onSave: (bio: Bio) => Promise<void>;
  onUploadImage: (file: File) => Promise<ImageBlock>;
  onResolveRecord: ResolveBioRecord;
}) {
  const { t } = useTranslation("settings");
  const [draft, setDraft] = useState(initialBio);
  const [dragging, setDragging] = useState(false);
  const [reducedMotion, setReducedMotion] = useState(false);
  useEffect(() => {
    const preference = window.matchMedia("(prefers-reduced-motion: reduce)");
    const update = () => setReducedMotion(preference.matches);
    update();
    preference.addEventListener("change", update);
    return () => preference.removeEventListener("change", update);
  }, []);
  const [edited, setEdited] = useState(false);
  const [saving, setSaving] = useState(false);
  const [uploading, setUploading] = useState(false);
  const [resolvingRecord, setResolvingRecord] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);
  const [preview, setPreview] = useState(false);
  const [documentErrors, setDocumentErrors] = useState<Record<string, string>>(
    {},
  );
  const setDocumentError = (key: string, error: string | null) =>
    setDocumentErrors((current) => {
      if (current[key] === (error ?? undefined)) return current;
      const next = { ...current };
      if (error) next[key] = error;
      else delete next[key];
      return next;
    });
  const invalidDocument = Object.values(documentErrors)[0];
  const [pending, setPending] = useState<{
    kind: BioInsertKind;
    editor: Editor;
    position: number;
    block?: Block;
    file?: File;
    alt?: string;
    url?: string;
  } | null>(null);
  const submitting = useRef(false);
  const [imagePreviews] = useState(() => new Map<string, string>());
  const mounted = useRef(true);
  const panels =
    draft.layout && place.stream.bio.layouts.panels.main.isTypeOf(draft.layout)
      ? draft.layout.panels
      : emptyPanels;
  const [panelKeys, setPanelKeys] = useState<string[]>(() =>
    panels.map(() => crypto.randomUUID()),
  );
  const busy = saving || uploading || resolvingRecord;
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      for (const url of imagePreviews.values()) URL.revokeObjectURL(url);
      imagePreviews.clear();
    };
  }, [imagePreviews]);
  const change = (next: Bio) => {
    setDraft(next);
    setEdited(true);
    setSaved(false);
    setError(null);
  };
  const changePanels = (next: Panel[]) => {
    for (const [cid, url] of imagePreviews) {
      if (
        !next.some((panel) =>
          panel.blocks.some(({ block }) => {
            if (!place.stream.bio.blocks.image.main.isTypeOf(block))
              return false;
            return (
              ("ref" in block.image
                ? block.image.ref.toString()
                : block.image.cid) === cid
            );
          }),
        )
      ) {
        URL.revokeObjectURL(url);
        imagePreviews.delete(cid);
      }
    }
    change({
      ...draft,
      layout: {
        ...draft.layout,
        $type: "place.stream.bio.layouts.panels",
        panels: next,
      },
    });
  };
  useBlocker({
    disabled: !edited,
    enableBeforeUnload: edited,
    shouldBlockFn: () =>
      !window.confirm(
        t(
          "bio-leave-unsaved",
          "Leave this page and discard your unsaved bio changes?",
        ),
      ),
  });
  const resolveRecord: ResolveBioRecord = async (source) => {
    setResolvingRecord(true);
    try {
      return await onResolveRecord(source);
    } finally {
      setResolvingRecord(false);
    }
  };
  const insert = (block: Block) => {
    if (!pending || pending.editor.isDestroyed) return;
    pending.editor
      .chain()
      .focus()
      .insertContentAt(pending.position, {
        type: "bioBlock",
        attrs: { entry: { block } },
      })
      .run();
    setPending(null);
  };
  const openInsert = (kind: BioInsertKind, editor: Editor) => {
    let block: Block | undefined;
    if (kind === "Link")
      block = {
        $type: "place.stream.bio.blocks.link",
        url: "" as Social["url"],
      };
    if (kind === "Embed")
      block = {
        $type: "place.stream.bio.blocks.embed",
        src: "" as place.stream.bio.blocks.embed.Main["src"],
      };
    if (kind === "Schedule")
      block = {
        $type: "place.stream.bio.blocks.schedule",
        timezone: Intl.DateTimeFormat().resolvedOptions().timeZone,
        slots: [],
      };
    if (kind === "Social links")
      block = { $type: "place.stream.bio.blocks.socialLinks", links: [] };
    const { selection } = editor.state;
    setPending({
      kind,
      editor,
      position:
        selection instanceof NodeSelection
          ? selection.to
          : selection.$from.depth > 1
            ? selection.$from.after(1)
            : selection.from,
      block,
    });
  };
  const save = async () => {
    if (submitting.current || !edited || busy || invalidDocument) return;
    submitting.current = true;
    setSaving(true);
    setError(null);
    setSaved(false);
    try {
      const record = prepareBioForSave(
        draft,
        new Date().toISOString() as Bio["createdAt"],
      );
      await onSave(record);
      setDraft(record);
      setEdited(false);
      setSaved(true);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      submitting.current = false;
      setSaving(false);
    }
  };
  return (
    <BioEditorBusyContext value={busy}>
      <div className="space-y-6">
        <div className="mx-auto flex w-full max-w-2xl flex-wrap items-center gap-2 px-4">
          <Button
            type="button"
            disabled={!edited || busy || !!invalidDocument}
            onClick={() => void save()}
          >
            {saving ? t("bio-saving", "Saving…") : t("save-bio", "Save bio")}
          </Button>
          <Button
            type="button"
            variant="secondary"
            aria-pressed={preview}
            onClick={() => setPreview(!preview)}
          >
            {preview ? <Pencil /> : <Eye />}
            {preview ? t("bio-edit", "Edit bio") : t("bio-preview", "Preview")}
          </Button>
          {saved && (
            <p role="status" className="text-success text-sm">
              {t("bio-saved", "Bio saved")}
            </p>
          )}
        </div>
        {!pending && (error || invalidDocument) && (
          <p
            role="alert"
            className="text-destructive mx-auto w-full max-w-2xl px-4 text-sm"
          >
            {error || invalidDocument}
          </p>
        )}
        {uploading && (
          <p role="status" className="mx-auto w-full max-w-2xl px-4">
            {t("bio-uploading", "Uploading image…")}
          </p>
        )}
        <div hidden={preview} className="space-y-6">
          <section className="mx-auto w-full max-w-2xl space-y-2 px-4">
            <h3 className="font-medium">{t("bio-about-you", "About you")}</h3>
            <BioDocumentEditor
              label="About you"
              initialContent={() => bioRichTextToDocument(draft.description)}
              disabled={busy}
              richTextOnly
              onError={(error) => setDocumentError("about", error)}
              onChange={(document) =>
                change({
                  ...draft,
                  description: documentToBioRichText(document),
                })
              }
            />
          </section>
          {draft.layout &&
          !place.stream.bio.layouts.panels.main.isTypeOf(draft.layout) ? (
            <p className="text-muted-foreground mx-auto w-full max-w-2xl px-4 text-sm">
              {t(
                "bio-unsupported-layout",
                "This imported layout is preserved. You can still edit your description and social links.",
              )}
            </p>
          ) : (
            <section
              className="mx-auto w-full max-w-[calc(var(--bio-panel-width)*5+var(--spacing)*24)] space-y-3 px-4"
              aria-label="Profile cards"
            >
              <div className="flex items-center justify-between gap-2">
                <h3 className="font-medium">{t("bio-cards", "Cards")}</h3>
                <Button
                  type="button"
                  variant="outline"
                  disabled={busy || panels.length >= 8}
                  onClick={() => {
                    const key = crypto.randomUUID();
                    setPanelKeys([...panelKeys, key]);
                    changePanels([...panels, { blocks: [] }]);
                  }}
                >
                  <Plus />
                  {t("bio-add-card", "Add card")}
                </Button>
              </div>
              <DragDropProvider
                onDragStart={() => setDragging(true)}
                // Keep masonry in place during a drag; repack when the card is dropped.
                onDragOver={(event) => event.preventDefault()}
                onDragEnd={(event) => {
                  setDragging(false);
                  const { source, target } = event.operation;
                  if (
                    event.canceled ||
                    busy ||
                    !isSortable(source) ||
                    !isSortable(target) ||
                    source.index === target.index
                  )
                    return;
                  setPanelKeys(
                    moveBioItem(panelKeys, source.index, target.index),
                  );
                  changePanels(moveBioItem(panels, source.index, target.index));
                }}
              >
                <BioPanelsLayout keepColumnsWhileFocused dragging={dragging}>
                  {panels.map((panel, index) => (
                    <BioPanelEditor
                      key={panelKeys[index]}
                      id={panelKeys[index]}
                      index={index}
                      count={panels.length}
                      disabled={busy}
                      onMove={(to) => {
                        setPanelKeys(moveBioItem(panelKeys, index, to));
                        changePanels(moveBioItem(panels, index, to));
                      }}
                      onRemove={() => {
                        const next = panels.filter((_, i) => i !== index);
                        const nextKeys = panelKeys.filter(
                          (_, i) => i !== index,
                        );
                        setDocumentError(panelKeys[index], null);
                        setPanelKeys(
                          nextKeys.length ? nextKeys : [crypto.randomUUID()],
                        );
                        changePanels(next.length ? next : [{ blocks: [] }]);
                      }}
                    >
                      <BioDocumentEditor
                        label="Bio content"
                        initialContent={() =>
                          panelBlocksToDocument(panel.blocks)
                        }
                        disabled={busy}
                        onError={(error) =>
                          setDocumentError(panelKeys[index], error)
                        }
                        onInsert={openInsert}
                        onChange={(document) =>
                          changePanels(
                            panels.map((entry, i) =>
                              i === index
                                ? {
                                    ...entry,
                                    blocks: documentToPanelBlocks(document),
                                  }
                                : entry,
                            ),
                          )
                        }
                        renderBlock={(entry, update, remove) => (
                          <BioAtomicBlock
                            entry={entry}
                            did={did}
                            imagePreviews={imagePreviews}
                            onChange={(next) => {
                              if (!submitting.current) update(next);
                            }}
                            onRemove={() => {
                              if (!submitting.current) remove();
                            }}
                            onResolveRecord={resolveRecord}
                          />
                        )}
                      />
                    </BioPanelEditor>
                  ))}
                </BioPanelsLayout>
                <DragOverlay dropAnimation={reducedMotion ? null : undefined}>
                  {(source) => {
                    const index = panelKeys.indexOf(String(source.id));
                    const panel = panels[index];
                    return panel ? (
                      <Card
                        data-bio-drag-preview
                        className="bg-card max-w-full space-y-4 p-4"
                        style={{
                          width: source.element?.getBoundingClientRect().width,
                        }}
                      >
                        <p className="font-medium">Card {index + 1}</p>
                        {panel.blocks.map((entry, i) => (
                          <BioBlockView
                            key={i}
                            block={entry.block}
                            did={did}
                            imagePreviews={imagePreviews}
                          />
                        ))}
                      </Card>
                    ) : null;
                  }}
                </DragOverlay>
              </DragDropProvider>
            </section>
          )}
          <div className="mx-auto w-full max-w-2xl px-4">
            <details className="rounded-lg border p-4">
              <summary className="cursor-pointer font-medium">
                {t("social-links", "Social links")}
              </summary>
              <fieldset disabled={busy} className="mt-4">
                <SocialFields
                  socials={draft.socials ?? []}
                  onChange={(socials) => change({ ...draft, socials })}
                />
              </fieldset>
            </details>
          </div>
        </div>
        {preview && (
          <section
            className="mx-auto w-full max-w-[calc(var(--bio-panel-width)*5+var(--spacing)*24)] px-4"
            aria-label="Bio preview"
          >
            <BioViewer bio={draft} did={did} imagePreviews={imagePreviews} />
          </section>
        )}
        <Dialog
          open={!!pending}
          onOpenChange={(open) => {
            if (!open && !busy) setPending(null);
          }}
        >
          <DialogContent>
            <DialogHeader>
              <DialogTitle>
                {pending ? `Add ${pending.kind.toLowerCase()}` : "Add block"}
              </DialogTitle>
            </DialogHeader>
            {error && (
              <p role="alert" className="text-destructive mb-4 text-sm">
                {error}
              </p>
            )}
            {pending?.kind === "Image" ? (
              <div className="space-y-4">
                <Field label="Add image file">
                  <Input
                    type="file"
                    accept="image/*"
                    disabled={busy}
                    onChange={(event) =>
                      setPending({ ...pending, file: event.target.files?.[0] })
                    }
                  />
                </Field>
                <Field label="Image description">
                  <Input
                    value={pending.alt ?? ""}
                    disabled={busy}
                    onChange={(event) =>
                      setPending({ ...pending, alt: event.target.value })
                    }
                  />
                </Field>
                <Field label={t("bio-image-link", "Image link (optional)")}>
                  <Input
                    value={pending.url ?? ""}
                    disabled={busy}
                    placeholder="https://…"
                    onChange={(event) =>
                      setPending({ ...pending, url: event.target.value })
                    }
                  />
                </Field>
                <Button
                  type="button"
                  disabled={!pending.file || busy}
                  onClick={async () => {
                    if (!pending.file) return;
                    setUploading(true);
                    setError(null);
                    try {
                      const url = pending.url?.trim() || undefined;
                      if (url && !getBioLinkUrl(url))
                        throw new Error(
                          "Links must use HTTP or HTTPS URLs, or mailto links.",
                        );
                      const block = {
                        ...(await onUploadImage(pending.file)),
                        $type: "place.stream.bio.blocks.image" as const,
                        alt: pending.alt ?? "",
                        url: url as ImageBlock["url"],
                      };
                      if (!mounted.current) return;
                      const cid =
                        "ref" in block.image
                          ? block.image.ref.toString()
                          : block.image.cid;
                      if (!imagePreviews.has(cid))
                        imagePreviews.set(
                          cid,
                          URL.createObjectURL(pending.file),
                        );
                      insert(block);
                    } catch (e) {
                      setError(e instanceof Error ? e.message : String(e));
                    } finally {
                      setUploading(false);
                    }
                  }}
                >
                  Insert image
                </Button>
              </div>
            ) : pending?.kind === "Bluesky post" ||
              pending?.kind === "Livestream" ? (
              <BioRecordReference
                collection={
                  pending.kind === "Bluesky post"
                    ? "app.bsky.feed.post"
                    : "place.stream.livestream"
                }
                onResolveRecord={resolveRecord}
                onChange={(reference) =>
                  insert(
                    pending.kind === "Bluesky post"
                      ? {
                          $type: "place.stream.bio.blocks.bskyPost",
                          post: reference,
                        }
                      : {
                          $type: "place.stream.bio.blocks.livestream",
                          livestream: reference,
                        },
                  )
                }
              />
            ) : (
              pending?.block && (
                <div className="space-y-4">
                  <BlockFields
                    block={pending.block}
                    onResolveRecord={resolveRecord}
                    onChange={(block) => setPending({ ...pending, block })}
                  />
                  <Button
                    type="button"
                    disabled={busy}
                    onClick={() => {
                      if (pending.block) insert(pending.block);
                    }}
                  >
                    Insert block
                  </Button>
                </div>
              )
            )}
          </DialogContent>
        </Dialog>
      </div>
    </BioEditorBusyContext>
  );
}
