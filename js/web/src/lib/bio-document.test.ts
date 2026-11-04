import {
  bioRichTextToDocument,
  documentToBioRichText,
  documentToPanelBlocks,
  panelBlocksToDocument,
} from "@streamplace/core";
import { getSchema, type JSONContent } from "@tiptap/core";
import StarterKit from "@tiptap/starter-kit";
import type { place } from "streamplace";
import { afterAll, describe, expect, it, vi } from "vitest";
import {
  BioBlock,
  BioFacet,
  BioMetadata,
} from "../components/bio/bio-document-editor";

type Entry = place.stream.bio.layouts.panels.BlockEntry;
const compatibilityWarnings = vi.hoisted(() =>
  vi.spyOn(console, "warn").mockImplementation(() => {}),
);
afterAll(() => {
  const messages = compatibilityWarnings.mock.calls.map(([message]) => message);
  compatibilityWarnings.mockRestore();
  expect(
    messages.every(
      (message) =>
        message ===
        "[@atproto/lex-data]: Uint8Array.fromBase64 / Uint8Array.prototype.toBase64 not available in this environment. Falling back to ponyfill implementation.",
    ),
  ).toBe(true);
});

type RichText = {
  plaintext: string;
  facets?: place.stream.bio.richtextFacet.Main[];
};

const text = (plaintext: string): JSONContent => ({
  type: "text",
  text: plaintext,
});
const paragraph = (plaintext: string): JSONContent => ({
  type: "paragraph",
  content: [text(plaintext)],
});

function reload(document: JSONContent): JSONContent {
  return JSON.parse(JSON.stringify(document));
}

describe("bio documents", () => {
  it("roundtrips through the actual editor schema with metadata and unsupported annotations intact", () => {
    const entries = [
      {
        alignment: "center",
        block: {
          $type: "place.stream.bio.blocks.text",
          plaintext: "🐱 café\nhello",
          textSize: "large",
          facets: [
            {
              index: { byteStart: 5, byteEnd: 10 },
              features: [
                {
                  $type: "app.bsky.richtext.facet#link",
                  uri: "mailto:creator@example.com",
                },
                { $type: "place.stream.bio.richtextFacet#underline" },
                { $type: "future.annotation", value: "retain" },
              ],
            },
          ],
        },
      },
      { block: { $type: "future.widget", value: 7 } },
    ] as unknown as Entry[];
    const schema = getSchema([
      StarterKit.configure({ codeBlock: false }),
      BioMetadata,
      BioFacet,
      BioBlock,
    ]);
    const actualJSON = schema
      .nodeFromJSON(panelBlocksToDocument(entries))
      .toJSON();
    expect(documentToPanelBlocks(actualJSON)).toEqual(entries);
    const original = entries[0].block as RichText;
    const about = schema.nodeFromJSON(bioRichTextToDocument(original)).toJSON();
    expect(documentToBioRichText(about)).toEqual(original);
  });
  it("roundtrips imported entries, opaque blocks and all metadata without changing them", () => {
    const entries: Entry[] = [
      {
        alignment: "center",
        futureEntry: "retained",
        block: {
          $type: "place.stream.bio.blocks.text",
          plaintext: "🐱 café\nhello",
          textSize: "large",
          futureBlock: { enabled: true },
          facets: [
            {
              index: { byteStart: 5, byteEnd: 10 },
              features: [
                { $type: "place.stream.bio.richtextFacet#bold" },
                { $type: "future.annotation", data: "retained" },
              ],
            },
          ],
        },
      } as Entry,
      {
        block: {
          $type: "place.stream.bio.blocks.header",
          plaintext: "heading",
        },
      },
      {
        block: {
          $type: "place.stream.bio.blocks.blockquote",
          plaintext: "quote\nline",
        },
      },
      {
        block: { $type: "place.stream.bio.blocks.divider" },
        alignment: "right",
      },
      {
        block: { $type: "future.widget", configuration: { value: 4 } },
        alignment: "left",
      } as unknown as Entry,
    ];
    const document = panelBlocksToDocument(entries);
    expect(document.content?.map((node) => node.type)).toEqual([
      "paragraph",
      "heading",
      "blockquote",
      "horizontalRule",
      "bioBlock",
    ]);
    expect(documentToPanelBlocks(reload(document))).toEqual(entries);
    expect(documentToPanelBlocks(document)[0]).toBe(entries[0]);
  });

  it("preserves metadata and moves unknown facets with their text after Unicode edits", () => {
    const entry = {
      alignment: "right",
      futureEntry: 7,
      block: {
        $type: "place.stream.bio.blocks.text",
        plaintext: "hello café",
        textSize: "small",
        futureBlock: "keep",
        facets: [
          {
            index: { byteStart: 6, byteEnd: 11 },
            features: [
              { $type: "place.stream.bio.richtextFacet#highlight" },
              {
                $type: "app.bsky.richtext.facet#mention",
                did: "did:plc:creator",
              },
              { $type: "future.annotation", label: "accent" },
            ],
          },
        ],
      },
    } as Entry;
    const document = panelBlocksToDocument([entry]);
    document.content![0].content!.unshift(text("🐱 "));
    const [edited] = documentToPanelBlocks(document);
    expect(edited).toEqual({
      ...entry,
      block: {
        ...entry.block,
        plaintext: "🐱 hello café",
        facets: [
          {
            index: { byteStart: 11, byteEnd: 16 },
            features: (entry.block as RichText).facets![0].features,
          },
        ],
      },
    });
    expect((entry.block as RichText).plaintext).toBe("hello café");
  });

  it("combines overlapping styles, retains hard breaks and recomputes UTF-8 offsets", () => {
    const document: JSONContent = {
      type: "doc",
      content: [
        {
          type: "paragraph",
          content: [
            { ...text("🐱 "), marks: [{ type: "bold" }] },
            { ...text("café"), marks: [{ type: "bold" }, { type: "italic" }] },
            { type: "hardBreak" },
            {
              ...text("email"),
              marks: [
                { type: "link", attrs: { href: "mailto:creator@example.com" } },
              ],
            },
          ],
        },
      ],
    };
    const value = documentToBioRichText(document);
    expect(value).toEqual({
      plaintext: "🐱 café\nemail",
      facets: [
        {
          index: { byteStart: 0, byteEnd: 10 },
          features: [{ $type: "place.stream.bio.richtextFacet#bold" }],
        },
        {
          index: { byteStart: 5, byteEnd: 10 },
          features: [{ $type: "place.stream.bio.richtextFacet#italic" }],
        },
        {
          index: { byteStart: 11, byteEnd: 16 },
          features: [
            {
              $type: "place.stream.bio.richtextFacet#link",
              uri: "mailto:creator@example.com",
            },
          ],
        },
      ],
    });
    expect(documentToBioRichText(reload(bioRichTextToDocument(value)))).toEqual(
      value,
    );
  });

  it("reuses original facets despite Tiptap mark ordering, defaults and text-node merging", () => {
    const original: RichText = {
      plaintext: "café",
      facets: [
        {
          index: { byteStart: 0, byteEnd: 5 },
          features: [
            {
              $type: "app.bsky.richtext.facet#link",
              uri: "https://example.com",
            },
            { $type: "place.stream.bio.richtextFacet#bold" },
          ],
        },
      ],
    };
    const document = bioRichTextToDocument(original);
    document.content![0].attrs = {
      ...document.content![0].attrs,
      bioEntry: null,
    };
    document.content![0].content![0].marks!.reverse();
    const link = document.content![0].content![0].marks!.find(
      (mark) => mark.type === "link",
    )!;
    link.attrs = {
      ...link.attrs,
      target: "_blank",
      rel: "noopener noreferrer nofollow",
      class: null,
    };
    expect(documentToBioRichText(document)).toBe(original);
  });

  it("merges repeated overlapping opaque annotations after an edit", () => {
    const feature = {
      $type: "future.annotation",
      id: "retained",
    } as unknown as place.stream.bio.richtextFacet.Main["features"][number];
    const document = bioRichTextToDocument({
      plaintext: "abc",
      facets: [
        { index: { byteStart: 0, byteEnd: 2 }, features: [feature] },
        { index: { byteStart: 1, byteEnd: 3 }, features: [feature] },
      ],
    });
    document.content![0].content!.unshift(text("x"));
    expect(documentToBioRichText(document)).toEqual({
      plaintext: "xabc",
      facets: [
        {
          index: { byteStart: 1, byteEnd: 4 },
          features: [feature],
        },
      ],
    });
  });

  it("keeps invalid byte ranges untouched without splitting Unicode or creating executable links", () => {
    const original: RichText = {
      plaintext: "🐱 unsafe",
      facets: [
        {
          index: { byteStart: 1, byteEnd: 4 },
          features: [{ $type: "place.stream.bio.richtextFacet#bold" }],
        },
        {
          index: { byteStart: 5, byteEnd: 11 },
          features: [
            {
              $type: "place.stream.bio.richtextFacet#link",
              uri: "javascript:alert(1)",
            },
          ],
        },
      ],
    };
    const document = bioRichTextToDocument(original);
    expect(
      document.content![0].content!.some((node) =>
        node.marks?.some((mark) => mark.type === "link"),
      ),
    ).toBe(false);
    expect(documentToBioRichText(reload(document))).toEqual(original);
  });

  it("edits ordinary nested lists while preserving item and content metadata", () => {
    const entry = {
      block: {
        $type: "place.stream.bio.blocks.orderedList",
        futureList: true,
        children: [
          {
            futureItem: "keep",
            content: {
              $type: "place.stream.bio.blocks.text",
              plaintext: "parent",
              textSize: "large",
            },
            unorderedListChildren: {
              children: [
                {
                  content: {
                    $type: "place.stream.bio.blocks.text",
                    plaintext: "child",
                    facets: [
                      {
                        index: { byteStart: 0, byteEnd: 99 },
                        features: [{ $type: "future.annotation" }],
                      },
                    ],
                  },
                },
              ],
            },
          },
        ],
      },
    } as unknown as Entry;
    const document = panelBlocksToDocument([entry]);
    expect(document.content![0].type).toBe("orderedList");
    expect(documentToPanelBlocks(reload(document))).toEqual([entry]);
    document.content![0].content![0].content![0].content = [text("edited")];
    const [edited] = documentToPanelBlocks(document);
    expect(edited).toEqual({
      block: {
        ...entry.block,
        children: [
          {
            ...(entry.block as place.stream.bio.blocks.orderedList.Main)
              .children[0],
            content: {
              $type: "place.stream.bio.blocks.text",
              plaintext: "edited",
              textSize: "large",
            },
          },
        ],
      },
    });
  });

  it("preserves untouched parent annotations and nested metadata when a child changes", () => {
    const entry = {
      block: {
        $type: "place.stream.bio.blocks.orderedList",
        children: [
          {
            content: {
              $type: "place.stream.bio.blocks.text",
              plaintext: "parent",
              facets: [
                {
                  index: { byteStart: 0, byteEnd: 99 },
                  features: [{ $type: "future.annotation" }],
                },
              ],
            },
            unorderedListChildren: {
              futureNested: true,
              children: [
                {
                  content: {
                    $type: "place.stream.bio.blocks.text",
                    plaintext: "child",
                  },
                },
              ],
            },
          },
        ],
      },
    } as unknown as Entry;
    const document = panelBlocksToDocument([entry]);
    document.content![0].content![0].content![1].content![0].content![0].content =
      [text("edited child")];
    const original = JSON.parse(JSON.stringify(entry)) as Entry;
    const item = (original.block as place.stream.bio.blocks.orderedList.Main)
      .children[0];
    item.unorderedListChildren!.children[0].content = {
      $type: "place.stream.bio.blocks.text",
      plaintext: "edited child",
    };
    expect(documentToPanelBlocks(document)).toEqual([original]);
  });

  it("preserves inactive imported nested branches while editing the visible list", () => {
    const entry = {
      block: {
        $type: "place.stream.bio.blocks.orderedList",
        children: [
          {
            content: {
              $type: "place.stream.bio.blocks.text",
              plaintext: "parent",
            },
            children: [
              {
                content: {
                  $type: "place.stream.bio.blocks.text",
                  plaintext: "visible",
                },
              },
            ],
            unorderedListChildren: {
              children: [
                {
                  content: {
                    $type: "place.stream.bio.blocks.text",
                    plaintext: "retained alternate",
                  },
                },
              ],
            },
          },
        ],
      },
    } as Entry;
    const document = panelBlocksToDocument([entry]);
    document.content![0].content![0].content![0].content = [text("edited")];
    expect(documentToPanelBlocks(document)).toEqual([
      {
        block: {
          ...entry.block,
          children: [
            {
              ...(entry.block as place.stream.bio.blocks.orderedList.Main)
                .children[0],
              content: {
                $type: "place.stream.bio.blocks.text",
                plaintext: "edited",
              },
            },
          ],
        },
      },
    ]);
  });

  it.each([false, true])(
    "changes list kinds with imported metadata (also changing nesting: %s)",
    (changeNested) => {
      const entry: Entry = {
        block: {
          $type: "place.stream.bio.blocks.orderedList",
          children: [
            {
              content: {
                $type: "place.stream.bio.blocks.text",
                plaintext: "parent",
              },
              unorderedListChildren: {
                children: [
                  {
                    content: {
                      $type: "place.stream.bio.blocks.text",
                      plaintext: "child",
                    },
                  },
                ],
              },
            },
          ],
        },
      };
      const document = panelBlocksToDocument([entry]);
      document.content![0].type = "bulletList";
      if (changeNested)
        document.content![0].content![0].content![1].type = "orderedList";
      expect(documentToPanelBlocks(document)).toEqual([
        {
          block: {
            $type: "place.stream.bio.blocks.unorderedList",
            children: [
              {
                content: {
                  $type: "place.stream.bio.blocks.text",
                  plaintext: "parent",
                },
                ...(changeNested
                  ? {
                      orderedListChildren: {
                        $type: "place.stream.bio.blocks.orderedList",
                        children: [
                          {
                            content: {
                              $type: "place.stream.bio.blocks.text",
                              plaintext: "child",
                            },
                          },
                        ],
                      },
                    }
                  : {
                      children: [
                        {
                          content: {
                            $type: "place.stream.bio.blocks.text",
                            plaintext: "child",
                          },
                        },
                      ],
                    }),
              },
            ],
          },
        },
      ]);
    },
  );

  it("keeps checklist and nonparagraph imported lists atomic", () => {
    const entries: Entry[] = [
      {
        block: {
          $type: "place.stream.bio.blocks.unorderedList",
          children: [
            {
              checked: false,
              content: {
                $type: "place.stream.bio.blocks.text",
                plaintext: "todo",
              },
            },
          ],
        },
      },
      {
        block: {
          $type: "place.stream.bio.blocks.orderedList",
          children: [
            {
              content: {
                $type: "place.stream.bio.blocks.header",
                plaintext: "heading",
                level: 2,
              },
            },
          ],
        },
      },
    ];
    const document = panelBlocksToDocument(entries);
    expect(document.content!.every((node) => node.type === "bioBlock")).toBe(
      true,
    );
    expect(documentToPanelBlocks(reload(document))).toEqual(entries);
  });

  it("creates the existing schema from new StarterKit paragraphs, headings, quotes and lists", () => {
    const document: JSONContent = {
      type: "doc",
      content: [
        paragraph("first"),
        {
          type: "heading",
          attrs: { level: 3 },
          content: [text("third level")],
        },
        { type: "blockquote", content: [paragraph("one"), paragraph("two")] },
        {
          type: "bulletList",
          content: [
            {
              type: "listItem",
              content: [paragraph("item"), paragraph("second paragraph")],
            },
          ],
        },
        { type: "horizontalRule" },
      ],
    };
    expect(documentToPanelBlocks(document)).toEqual([
      { block: { $type: "place.stream.bio.blocks.text", plaintext: "first" } },
      {
        block: {
          $type: "place.stream.bio.blocks.header",
          plaintext: "third level",
          level: 3,
        },
      },
      {
        block: {
          $type: "place.stream.bio.blocks.blockquote",
          plaintext: "one\ntwo",
        },
      },
      {
        block: {
          $type: "place.stream.bio.blocks.unorderedList",
          children: [
            {
              content: {
                $type: "place.stream.bio.blocks.text",
                plaintext: "item\nsecond paragraph",
              },
            },
          ],
        },
      },
      { block: { $type: "place.stream.bio.blocks.divider" } },
    ]);
  });

  it("distinguishes empty new documents from intentionally saved empty paragraphs", () => {
    expect(documentToPanelBlocks(panelBlocksToDocument([]))).toEqual([]);
    const entries: Entry[] = [
      { block: { $type: "place.stream.bio.blocks.text", plaintext: "" } },
    ];
    expect(documentToPanelBlocks(panelBlocksToDocument(entries))).toEqual(
      entries,
    );
    expect(documentToBioRichText(bioRichTextToDocument())).toEqual({
      plaintext: "",
    });
  });

  it("joins about-you paragraphs with newlines and places marks after their UTF-8 separator", () => {
    const document: JSONContent = {
      type: "doc",
      content: [
        paragraph("🐱"),
        {
          type: "paragraph",
          content: [
            {
              ...text("café"),
              marks: [
                { type: "strike" },
                { type: "code" },
                { type: "underline" },
              ],
            },
          ],
        },
      ],
    };
    expect(documentToBioRichText(document)).toEqual({
      plaintext: "🐱\ncafé",
      facets: [
        {
          index: { byteStart: 5, byteEnd: 10 },
          features: [
            { $type: "place.stream.bio.richtextFacet#strikethrough" },
            { $type: "place.stream.bio.richtextFacet#code" },
            { $type: "place.stream.bio.richtextFacet#underline" },
          ],
        },
      ],
    });
  });

  it("rejects heading formatting inside quotes that the bio schema cannot represent", () => {
    expect(() =>
      documentToPanelBlocks({
        type: "doc",
        content: [
          {
            type: "blockquote",
            content: [
              {
                type: "heading",
                attrs: { level: 2 },
                content: [text("heading")],
              },
            ],
          },
        ],
      }),
    ).toThrow("Quotes can only contain text paragraphs.");
  });

  it("rejects unrepresentable document structures instead of silently losing them", () => {
    expect(() =>
      documentToPanelBlocks({
        type: "doc",
        content: [{ type: "unknownNode" }],
      }),
    ).toThrow("This type of content cannot be saved in a bio");
    expect(() =>
      documentToPanelBlocks({ type: "doc", content: [{ type: "bioBlock" }] }),
    ).toThrow("This block is missing its saved content.");
    expect(() =>
      documentToPanelBlocks({
        type: "doc",
        content: [
          {
            type: "orderedList",
            attrs: { start: 3 },
            content: [{ type: "listItem", content: [paragraph("third")] }],
          },
        ],
      }),
    ).toThrow("Ordered bio lists must start at 1.");
    expect(() =>
      documentToBioRichText({
        type: "doc",
        content: [{ type: "heading", content: [text("heading")] }],
      }),
    ).toThrow("About you supports text paragraphs only.");
  });
});

it("rebases imported feature, facet and index extensions after Unicode text edits", () => {
  const facet = {
    futureFacet: "retain",
    index: { byteStart: 0, byteEnd: 4, futureIndex: 7 },
    features: [
      { $type: "place.stream.bio.richtextFacet#bold", futureFeature: true },
    ],
  };
  const entry = {
    block: {
      $type: "place.stream.bio.blocks.text",
      plaintext: "bold",
      facets: [facet],
    },
  } as unknown as Entry;
  const schema = getSchema([StarterKit, BioMetadata, BioFacet, BioBlock]);
  const document = schema.nodeFromJSON(panelBlocksToDocument([entry])).toJSON();
  document.content![0].content![0].text = "🐱 bolder";
  expect(documentToPanelBlocks(document)).toEqual([
    {
      block: {
        ...entry.block,
        plaintext: "🐱 bolder",
        facets: [{ ...facet, index: { ...facet.index, byteEnd: 11 } }],
      },
    },
  ]);
  document.content![0].content![0].marks = [];
  expect(
    (documentToPanelBlocks(document)[0].block as RichText).facets,
  ).toBeUndefined();
});
