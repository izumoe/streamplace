import type { JSONContent } from "@tiptap/core";
import type { place } from "streamplace";

type Entry = place.stream.bio.layouts.panels.BlockEntry;
type RichText = {
  plaintext: string;
  facets?: place.stream.bio.richtextFacet.Main[];
};
type Facet = place.stream.bio.richtextFacet.Main;
type Feature = Facet["features"][number];
type Annotation = { feature: Feature; facet?: Facet };
type List =
  | place.stream.bio.blocks.orderedList.Main
  | place.stream.bio.blocks.unorderedList.Main;
type ListItem =
  | place.stream.bio.blocks.orderedList.ListItem
  | place.stream.bio.blocks.unorderedList.ListItem;

const blockPrefix = "place.stream.bio.blocks.";
const facetPrefix = "place.stream.bio.richtextFacet#";
const styles: Record<string, string> = {
  bold: "bold",
  italic: "italic",
  strike: "strikethrough",
  code: "code",
  underline: "underline",
};
const utf8 = new TextEncoder();
const styleMarks = new Map(
  Object.entries(styles).map(([mark, suffix]) => [
    `${facetPrefix}${suffix}`,
    mark,
  ]),
);

export function getBioLinkUrl(href: string | undefined): string | undefined {
  if (!href) return undefined;
  try {
    const url = new URL(href);
    if (
      url.protocol === "http:" ||
      url.protocol === "https:" ||
      url.protocol === "mailto:"
    )
      return url.href;
  } catch {}
  return undefined;
}

function inlineContent({ plaintext, facets = [] }: RichText): JSONContent[] {
  const boundaries = new Map<number, number>([[0, 0]]);
  let bytes = 0;
  let chars = 0;
  for (const character of plaintext) {
    const point = character.codePointAt(0)!;
    bytes += point < 0x80 ? 1 : point < 0x800 ? 2 : point < 0x10000 ? 3 : 4;
    chars += character.length;
    boundaries.set(bytes, chars);
  }
  const valid = facets.filter(
    ({ index }) =>
      index.byteStart < index.byteEnd &&
      boundaries.has(index.byteStart) &&
      boundaries.has(index.byteEnd),
  );
  const splits = [
    ...new Set([
      0,
      bytes,
      ...valid.flatMap(({ index }) => [index.byteStart, index.byteEnd]),
    ]),
  ].sort((a, b) => a - b);
  const content: JSONContent[] = [];
  for (let i = 0; i < splits.length - 1; i++) {
    const start = splits[i];
    const end = splits[i + 1];
    const features = valid
      .filter(({ index }) => index.byteStart <= start && index.byteEnd >= end)
      .flatMap((facet) =>
        facet.features.map((feature) => ({ feature, facet })),
      );
    const marks: NonNullable<JSONContent["marks"]> = [];
    const opaque: Annotation[] = [];
    for (const annotation of features) {
      const { feature } = annotation;
      const style = styleMarks.get(feature.$type);
      if (style) {
        const existing = marks.find((mark) => mark.type === style);
        if (existing) existing.attrs!.bioAnnotations.push(annotation);
        else
          marks.push({ type: style, attrs: { bioAnnotations: [annotation] } });
      } else if (
        (feature.$type === `${facetPrefix}link` ||
          feature.$type === "app.bsky.richtext.facet#link") &&
        "uri" in feature &&
        typeof feature.uri === "string" &&
        getBioLinkUrl(feature.uri) &&
        !marks.some((mark) => mark.type === "link")
      ) {
        marks.push({
          type: "link",
          attrs: { href: feature.uri, bioAnnotations: [annotation] },
        });
      } else opaque.push(annotation);
    }
    if (opaque.length)
      marks.push({
        type: "bioFacet",
        attrs: {
          features: opaque.map(({ feature }) => feature),
          bioAnnotations: opaque,
        },
      });
    const parts = plaintext
      .slice(boundaries.get(start), boundaries.get(end))
      .split("\n");
    parts.forEach((part, partIndex) => {
      if (partIndex)
        content.push({ type: "hardBreak", ...(marks.length ? { marks } : {}) });
      if (part)
        content.push({
          type: "text",
          text: part,
          ...(marks.length ? { marks } : {}),
        });
    });
  }
  return content;
}

function featuresForMarks(marks: JSONContent["marks"] = []): Annotation[] {
  return marks.flatMap((mark): Annotation[] => {
    const imported: Annotation[] = mark.attrs?.bioAnnotations ?? [];
    if (styles[mark.type]) {
      const type = `${facetPrefix}${styles[mark.type]}` as Feature["$type"];
      return imported.length
        ? imported.map(({ feature, facet }) => ({
            feature: { ...feature, $type: type } as Feature,
            facet,
          }))
        : [{ feature: { $type: type } as Feature }];
    }
    if (mark.type === "link" && typeof mark.attrs?.href === "string")
      return imported.length
        ? imported.map(({ feature, facet }) => ({
            feature: { ...feature, uri: mark.attrs!.href } as Feature,
            facet,
          }))
        : [
            {
              feature: {
                $type: `${facetPrefix}link`,
                uri: mark.attrs.href,
              } as Feature,
            },
          ];
    if (mark.type === "bioFacet" && Array.isArray(mark.attrs?.features))
      return mark.attrs.features.flatMap((feature: Feature) => {
        const originals = imported.filter(
          (annotation) =>
            JSON.stringify(annotation.feature) === JSON.stringify(feature),
        );
        return originals.length ? originals : [{ feature }];
      });
    throw new Error(
      `This text contains an unsupported annotation: ${mark.type}.`,
    );
  });
}

function richTextFromNodes(nodes: JSONContent[]): RichText {
  let plaintext = "";
  let bytes = 0;
  const spans = new Map<string, Facet[]>();
  const append = (text: string, marks?: JSONContent["marks"]) => {
    const start = bytes;
    plaintext += text;
    bytes += utf8.encode(text).length;
    if (start === bytes) return;
    for (const { feature, facet } of featuresForMarks(marks)) {
      const { index, features, ...metadata } = facet ?? {};
      const { byteStart, byteEnd, ...indexMetadata } = index ?? {};
      const key = JSON.stringify({ feature, metadata, indexMetadata });
      const ranges = spans.get(key) ?? [];
      const previous = ranges.at(-1);
      if (previous && previous.index.byteEnd >= start)
        previous.index.byteEnd = bytes;
      else
        ranges.push({
          ...metadata,
          index: { ...indexMetadata, byteStart: start, byteEnd: bytes },
          features: [feature],
        });
      spans.set(key, ranges);
    }
  };
  nodes.forEach((node, index) => {
    if (index) append("\n");
    for (const child of node.content ?? []) {
      if (child.type === "text") append(child.text ?? "", child.marks);
      else if (child.type === "hardBreak") append("\n", child.marks);
      else throw new Error("This text contains unsupported inline content.");
    }
  });
  const grouped = new Map<string, Facet>();
  for (const ranges of spans.values())
    for (const range of ranges) {
      const { features, ...metadata } = range;
      const key = JSON.stringify(metadata);
      const previous = grouped.get(key);
      if (previous) previous.features.push(...range.features);
      else grouped.set(key, range);
    }
  const facets = [...grouped.values()];
  return facets.length ? { plaintext, facets } : { plaintext };
}

// Tiptap adds default attrs, orders marks and merges adjacent text nodes.
export function bioDocumentSignature(node: JSONContent): string {
  const children: {
    type?: string;
    text?: string;
    marks?: string;
    value?: string;
  }[] = [];
  for (const child of node.content ?? []) {
    if (child.type === "text" || child.type === "hardBreak") {
      const marks = JSON.stringify(
        (child.marks ?? [])
          .map((mark) => ({
            type: mark.type,
            ...(mark.type === "link"
              ? { href: mark.attrs?.href }
              : mark.type === "bioFacet"
                ? { features: mark.attrs?.features }
                : {}),
          }))
          .sort((a, b) => JSON.stringify(a).localeCompare(JSON.stringify(b))),
      );
      const text = child.type === "hardBreak" ? "\n" : (child.text ?? "");
      const previous = children.at(-1);
      if (previous?.type === "text" && previous.marks === marks)
        previous.text += text;
      else children.push({ type: "text", text, marks });
    } else children.push({ value: bioDocumentSignature(child) });
  }
  return JSON.stringify({
    type: node.type,
    ...(node.type === "heading" ? { level: node.attrs?.level ?? 1 } : {}),
    children,
  });
}

export function bioRichTextToDocument(
  value: place.stream.bio.page.Description = { plaintext: "" },
): JSONContent {
  return {
    type: "doc",
    content: [
      {
        type: "paragraph",
        attrs: { bioRichText: value },
        content: inlineContent(value),
      },
    ],
  };
}

export function documentToBioRichText(
  document: JSONContent,
): place.stream.bio.page.Description {
  const original = document.content?.[0]?.attrs?.bioRichText as
    RichText | undefined;
  if (
    original &&
    bioDocumentSignature(document) ===
      bioDocumentSignature(bioRichTextToDocument(original))
  )
    return original;
  if (document.content?.some((node) => node.type !== "paragraph"))
    throw new Error("About you supports text paragraphs only.");
  const { facets, ...rest } = original ?? { plaintext: "" };
  return { ...rest, ...richTextFromNodes(document.content ?? []) };
}

function editableList(block: List): boolean {
  return (
    block.children.length > 0 &&
    block.children.every((item) => {
      if ("checked" in item || item.content.$type !== `${blockPrefix}text`)
        return false;
      if (item.children)
        return editableList({ ...block, children: item.children } as List);
      const nested =
        "unorderedListChildren" in item
          ? item.unorderedListChildren
          : "orderedListChildren" in item
            ? item.orderedListChildren
            : undefined;
      return !nested || editableList(nested);
    })
  );
}

function listToNode(block: List, ordered: boolean, entry: Entry): JSONContent {
  return {
    type: ordered ? "orderedList" : "bulletList",
    attrs: { bioEntry: entry },
    content: block.children.map((item) => {
      const nested = item.children
        ? listToNode({ children: item.children } as List, ordered, {
            block: {
              $type: `${blockPrefix}${ordered ? "orderedList" : "unorderedList"}`,
              children: item.children,
            },
          } as Entry)
        : "unorderedListChildren" in item && item.unorderedListChildren
          ? listToNode(item.unorderedListChildren, false, {
              block: item.unorderedListChildren,
            } as Entry)
          : "orderedListChildren" in item && item.orderedListChildren
            ? listToNode(item.orderedListChildren, true, {
                block: item.orderedListChildren,
              } as Entry)
            : undefined;
      return {
        type: "listItem",
        attrs: { bioListItem: item },
        content: [
          {
            type: "paragraph",
            content: inlineContent(item.content as RichText),
          },
          ...(nested ? [nested] : []),
        ],
      };
    }),
  };
}

function entryToNode(entry: Entry): JSONContent {
  const { block } = entry;
  const attrs = { bioEntry: entry };
  switch (block.$type) {
    case `${blockPrefix}text`:
      return {
        type: "paragraph",
        attrs,
        content: inlineContent(block as RichText),
      };
    case `${blockPrefix}header`: {
      const heading = block as place.stream.bio.blocks.header.Main;
      return {
        type: "heading",
        attrs: { ...attrs, level: heading.level ?? 1 },
        content: inlineContent(heading),
      };
    }
    case `${blockPrefix}blockquote`:
      return {
        type: "blockquote",
        attrs,
        content: [
          { type: "paragraph", content: inlineContent(block as RichText) },
        ],
      };
    case `${blockPrefix}divider`:
      return { type: "horizontalRule", attrs };
    case `${blockPrefix}orderedList`:
    case `${blockPrefix}unorderedList`:
      if (editableList(block as List))
        return listToNode(
          block as List,
          block.$type === `${blockPrefix}orderedList`,
          entry,
        );
  }
  return { type: "bioBlock", attrs: { entry } };
}

export function panelBlocksToDocument(blocks: Entry[]): JSONContent {
  return {
    type: "doc",
    content: blocks.length ? blocks.map(entryToNode) : [{ type: "paragraph" }],
  };
}

function listFromNode(node: JSONContent, originalOrdered?: boolean): List {
  const ordered = node.type === "orderedList";
  if (ordered && node.attrs?.start != null && node.attrs.start !== 1)
    throw new Error("Ordered bio lists must start at 1.");
  const original = (node.attrs?.bioEntry as Entry | undefined)?.block as
    List | undefined;
  const wasOrdered = original?.$type
    ? original.$type === `${blockPrefix}orderedList`
    : (originalOrdered ?? ordered);
  if (
    original &&
    wasOrdered === ordered &&
    bioDocumentSignature(node) ===
      bioDocumentSignature(
        listToNode(original, ordered, { block: original } as Entry),
      )
  )
    return original;
  const children = (node.content ?? []).map((item): ListItem => {
    if (item.type !== "listItem")
      throw new Error("Lists can only contain list items.");
    const previous = item.attrs?.bioListItem as ListItem | undefined;
    if (
      previous &&
      wasOrdered === ordered &&
      bioDocumentSignature(item) ===
        bioDocumentSignature(
          listToNode({ children: [previous] } as List, ordered, {} as Entry)
            .content![0],
        )
    )
      return previous;
    const paragraphs = (item.content ?? []).filter(
      (child) => child.type === "paragraph",
    );
    const nested = (item.content ?? []).filter(
      (child) => child.type === "bulletList" || child.type === "orderedList",
    );
    if (
      !paragraphs.length ||
      paragraphs.length + nested.length !== item.content?.length ||
      nested.length > 1 ||
      (nested.length && item.content!.at(-1) !== nested[0])
    )
      throw new Error("Keep list item text before a single nested list.");
    const {
      children: oldChildren,
      unorderedListChildren,
      orderedListChildren,
      ...rest
    } = (previous ?? {}) as ListItem & {
      unorderedListChildren?: List;
      orderedListChildren?: List;
    };
    const { facets, ...oldContent } = (previous?.content as RichText) ?? {
      plaintext: "",
    };
    const unchangedContent =
      previous &&
      paragraphs.length === 1 &&
      bioDocumentSignature(paragraphs[0]) ===
        bioDocumentSignature({
          type: "paragraph",
          content: inlineContent(previous.content as RichText),
        });
    const next: ListItem = {
      ...rest,
      content: unchangedContent
        ? previous.content
        : {
            ...oldContent,
            $type: `${blockPrefix}text`,
            ...richTextFromNodes(paragraphs),
          },
    };
    if (nested.length) {
      const previousNestedOrdered = oldChildren
        ? wasOrdered
        : orderedListChildren
          ? true
          : unorderedListChildren
            ? false
            : undefined;
      const value = listFromNode(nested[0], previousNestedOrdered);
      if ((nested[0].type === "orderedList") === ordered) {
        next.children = value.children as ListItem["children"];
        if (oldChildren && wasOrdered === ordered) {
          if (ordered && unorderedListChildren)
            (
              next as place.stream.bio.blocks.orderedList.ListItem
            ).unorderedListChildren =
              unorderedListChildren as place.stream.bio.blocks.unorderedList.Main;
          else if (!ordered && orderedListChildren)
            (
              next as place.stream.bio.blocks.unorderedList.ListItem
            ).orderedListChildren =
              orderedListChildren as place.stream.bio.blocks.orderedList.Main;
        }
      } else if (ordered)
        (
          next as place.stream.bio.blocks.orderedList.ListItem
        ).unorderedListChildren =
          value as place.stream.bio.blocks.unorderedList.Main;
      else
        (
          next as place.stream.bio.blocks.unorderedList.ListItem
        ).orderedListChildren =
          value as place.stream.bio.blocks.orderedList.Main;
    }
    return next;
  });
  return {
    ...(wasOrdered === ordered ? original : {}),
    ...(wasOrdered === ordered && original && !original.$type
      ? {}
      : {
          $type: `${blockPrefix}${ordered ? "orderedList" : "unorderedList"}`,
        }),
    children,
  } as List;
}

function nodeToEntry(node: JSONContent): Entry {
  if (node.type === "bioBlock") {
    if (!node.attrs?.entry?.block)
      throw new Error("This block is missing its saved content.");
    return node.attrs.entry;
  }
  const original = node.attrs?.bioEntry as Entry | undefined;
  if (
    original &&
    bioDocumentSignature(node) === bioDocumentSignature(entryToNode(original))
  )
    return original;
  let block: Record<string, unknown>;
  const type = `${blockPrefix}${node.type === "paragraph" ? "text" : node.type === "heading" ? "header" : node.type}`;
  const base = original?.block.$type === type ? original.block : {};
  switch (node.type) {
    case "paragraph":
    case "heading":
    case "blockquote": {
      const nodes = node.type === "blockquote" ? (node.content ?? []) : [node];
      if (
        node.type === "blockquote" &&
        nodes.some((child) => child.type !== "paragraph")
      )
        throw new Error("Quotes can only contain text paragraphs.");
      const { facets, ...rest } = base as RichText;
      block = { ...rest, $type: type, ...richTextFromNodes(nodes) };
      if (node.type === "heading") {
        const level = node.attrs?.level ?? 1;
        if (![1, 2, 3].includes(level))
          throw new Error("Headings can only use levels 1 to 3.");
        block = { ...block, level };
      }
      break;
    }
    case "horizontalRule":
      block = {
        ...(original?.block.$type === `${blockPrefix}divider`
          ? original.block
          : {}),
        $type: `${blockPrefix}divider`,
      };
      break;
    case "bulletList":
    case "orderedList":
      block = {
        ...listFromNode(node),
        $type: `${blockPrefix}${node.type === "orderedList" ? "orderedList" : "unorderedList"}`,
      };
      break;
    default:
      throw new Error(
        `This type of content cannot be saved in a bio: ${node.type}.`,
      );
  }
  return { ...original, block: block as Entry["block"] };
}

export function documentToPanelBlocks(document: JSONContent): Entry[] {
  const nodes = document.content ?? [];
  if (
    nodes.length === 1 &&
    nodes[0].type === "paragraph" &&
    !nodes[0].attrs?.bioEntry &&
    !nodes[0].content?.length
  )
    return [];
  return nodes.map(nodeToEntry);
}
