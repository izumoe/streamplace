import { jsonToLex } from "@atproto/lex";
import {
  moveBioItem,
  prepareBioForSave,
  setBioPlaintext,
} from "@streamplace/core";
import { place } from "streamplace";
import { afterAll, describe, expect, it, vi } from "vitest";

const createdAt = "2026-05-18T12:00:00Z";
const updatedAt = "2026-10-02T12:00:00Z";

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

describe("bio editing", () => {
  it.each([
    {
      name: "image blocks",
      $type: "place.stream.bio.blocks.image",
      field: "image",
      aspectRatio: { width: 4, height: 3 },
    },
    {
      name: "link previews",
      $type: "place.stream.bio.blocks.link",
      field: "previewImage",
      url: "https://example.com/support",
    },
    {
      name: "embed previews",
      $type: "place.stream.bio.blocks.embed",
      field: "previewImage",
      src: "https://example.com/player",
    },
  ])(
    "loads and preserves images up to 5 MB in $name",
    ({ name: _name, field, ...block }) => {
      for (const size of [1_963_404, 5_000_000, 5_000_001]) {
        const image = jsonToLex({
          $type: "blob",
          ref: {
            $link:
              "bafkreigh2akiscaildcgjd5thb4cc2pmrm3g4jcj3rkdxlixjxznyjivnm",
          },
          mimeType: "image/png",
          size,
        });
        const record = {
          $type: "place.stream.bio.page",
          createdAt,
          layout: {
            $type: "place.stream.bio.layouts.panels",
            panels: [{ blocks: [{ block: { ...block, [field]: image } }] }],
          },
        };
        if (size > 5_000_000) {
          expect(() => place.stream.bio.page.$parse(record)).toThrow(
            /blob too big/,
          );
          continue;
        }
        const draft = place.stream.bio.page.$parse(record);
        const saved = prepareBioForSave(
          { ...draft, description: { plaintext: "Updated introduction" } },
          updatedAt,
        );
        expect(saved.description?.plaintext).toBe("Updated introduction");
        expect(saved.layout).toEqual(draft.layout);
      }
    },
  );

  it("still rejects malformed and non-image blob references", () => {
    for (const image of [
      { $type: "blob", mimeType: "image/png", size: 1_963_404 },
      jsonToLex({
        $type: "blob",
        ref: {
          $link: "bafkreigh2akiscaildcgjd5thb4cc2pmrm3g4jcj3rkdxlixjxznyjivnm",
        },
        mimeType: "application/pdf",
        size: 1_963_404,
      }),
    ]) {
      expect(() =>
        place.stream.bio.blocks.image.$parse({
          image,
          aspectRatio: { width: 4, height: 3 },
        }),
      ).toThrow();
    }
  });

  it.each([
    { url: "javascript:alert(1)", inList: false },
    { url: "data:text/html,hello", inList: false },
    { url: "javascript:alert(1)", inList: true },
  ])(
    "rejects unsafe image destinations: $url (in list: $inList)",
    ({ url, inList }) => {
      const image = {
        $type: "place.stream.bio.blocks.image",
        image: jsonToLex({
          $type: "blob",
          ref: {
            $link:
              "bafkreigh2akiscaildcgjd5thb4cc2pmrm3g4jcj3rkdxlixjxznyjivnm",
          },
          mimeType: "image/png",
          size: 64,
        }),
        aspectRatio: { width: 2, height: 1 },
        url,
      };
      const block = inList
        ? {
            $type: "place.stream.bio.blocks.unorderedList",
            children: [
              {
                content: {
                  $type: "place.stream.bio.blocks.text",
                  plaintext: "Support",
                },
                children: [{ content: image }],
              },
            ],
          }
        : image;
      const draft = place.stream.bio.page.$parse({
        $type: "place.stream.bio.page",
        createdAt,
        layout: {
          $type: "place.stream.bio.layouts.panels",
          panels: [{ blocks: [{ block }] }],
        },
      });
      expect(() => prepareBioForSave(draft, updatedAt)).toThrow(/https?/i);
    },
  );

  it("validates existing facets without adding schema fields to their saved data", () => {
    const draft: place.stream.bio.page.Main = {
      $type: "place.stream.bio.page",
      createdAt,
      layout: {
        $type: "place.stream.bio.layouts.panels",
        panels: [
          {
            blocks: [
              {
                block: {
                  $type: "place.stream.bio.blocks.text",
                  plaintext: "bold",
                  facets: [
                    {
                      index: { byteStart: 0, byteEnd: 4 },
                      features: [
                        { $type: "place.stream.bio.richtextFacet#bold" },
                      ],
                    },
                  ],
                },
              },
            ],
          },
        ],
      },
    };
    const original = structuredClone(draft.layout);
    expect(prepareBioForSave(draft, updatedAt).layout).toEqual(original);
    expect(draft.layout).toEqual(original);
  });

  it("keeps annotations until their text changes, then removes stale byte ranges", () => {
    const block = {
      $type: "place.stream.bio.blocks.text" as const,
      plaintext: "🐱 café",
      textSize: "large" as const,
      facets: [
        {
          index: { byteStart: 5, byteEnd: 10 },
          features: [{ $type: "place.stream.bio.richtextFacet#bold" as const }],
        },
      ],
    };
    expect(setBioPlaintext(block, block.plaintext)).toBe(block);
    expect(setBioPlaintext(block, "hello")).toEqual({
      $type: block.$type,
      plaintext: "hello",
      textSize: "large",
    });
    expect(block.facets).toHaveLength(1);
  });

  it("moves whole entries without losing imported blocks or alignment", () => {
    const entries = [
      { alignment: "right", block: { $type: "example.bio.widget", value: 7 } },
      { block: { $type: "place.stream.bio.blocks.divider" } },
    ];
    expect(moveBioItem(entries, 0, 1)).toEqual([entries[1], entries[0]]);
    expect(moveBioItem(entries, 0, -1)).toBe(entries);
    expect(moveBioItem(entries, 1, 2)).toBe(entries);
    expect(entries[0].block.value).toBe(7);
  });

  it("saves the full draft with provenance and original creation time", () => {
    const draft = place.stream.bio.page.$parse({
      $type: "place.stream.bio.page",
      createdAt,
      importedFrom: "at://did:plc:creator/site.standard.document/abc",
      description: { plaintext: "about" },
      layout: {
        $type: "place.stream.bio.layouts.panels",
        panels: [
          {
            blocks: [
              {
                alignment: "center",
                block: { $type: "example.bio.widget", value: 7 },
              },
            ],
          },
        ],
      },
    });
    expect(prepareBioForSave(draft, updatedAt)).toEqual({
      ...draft,
      updatedAt,
    });
    expect(draft.updatedAt).toBeUndefined();
  });

  it("allows deleting the last panel without writing an invalid empty layout", () => {
    const record = prepareBioForSave(
      {
        $type: "place.stream.bio.page",
        createdAt,
        layout: { $type: "place.stream.bio.layouts.panels", panels: [] },
      },
      updatedAt,
    );
    expect(record.layout).toBeUndefined();
    expect(place.stream.bio.page.$matches(record)).toBe(true);
  });

  it("preserves imported mailto links when editing the description", () => {
    const imported = place.stream.bio.page.$parse({
      $type: "place.stream.bio.page",
      createdAt,
      description: { plaintext: "imported description" },
      socials: [{ platform: "email", url: "mailto:creator@example.com" }],
      layout: {
        $type: "place.stream.bio.layouts.panels",
        panels: [
          {
            blocks: [
              {
                block: {
                  $type: "place.stream.bio.blocks.link",
                  url: "mailto:booking@example.com?subject=Booking",
                },
              },
              {
                block: {
                  $type: "place.stream.bio.blocks.socialLinks",
                  links: [
                    { platform: "email", url: "mailto:hello@example.com" },
                  ],
                },
              },
            ],
          },
        ],
      },
    });
    const draft = {
      ...imported,
      description: setBioPlaintext(imported.description!, "edited description"),
    };
    expect(prepareBioForSave(draft, updatedAt)).toEqual({
      ...draft,
      updatedAt,
    });
  });

  it("requires an HTTP or HTTPS source for embeds", () => {
    expect(() =>
      prepareBioForSave(
        {
          $type: "place.stream.bio.page",
          createdAt,
          layout: {
            $type: "place.stream.bio.layouts.panels",
            panels: [
              {
                blocks: [
                  {
                    block: {
                      $type: "place.stream.bio.blocks.embed",
                      src: "mailto:creator@example.com",
                    },
                  },
                ],
              },
            ],
          },
        },
        updatedAt,
      ),
    ).toThrow(/https?/i);
  });

  it("rejects schema limits and unsafe outbound URLs before saving", () => {
    expect(() =>
      prepareBioForSave(
        {
          $type: "place.stream.bio.page",
          createdAt,
          description: { plaintext: "x".repeat(3001) },
        },
        updatedAt,
      ),
    ).toThrow();
    expect(() =>
      prepareBioForSave(
        {
          $type: "place.stream.bio.page",
          createdAt,
          socials: [{ platform: "website", url: "javascript:alert(1)" }],
        },
        updatedAt,
      ),
    ).toThrow(/https?/i);
    expect(() =>
      prepareBioForSave(
        {
          $type: "place.stream.bio.page",
          createdAt,
          layout: {
            $type: "place.stream.bio.layouts.panels",
            panels: [
              {
                blocks: [
                  {
                    block: {
                      $type: "place.stream.bio.blocks.link",
                      url: "data:text/html,hello",
                    },
                  },
                ],
              },
            ],
          },
        },
        updatedAt,
      ),
    ).toThrow(/https?/i);
  });
});

it("preserves unknown root, layout, entry, block and facet data when saving", () => {
  const draft = {
    $type: "place.stream.bio.page",
    createdAt,
    futureRoot: { enabled: true },
    layout: {
      $type: "place.stream.bio.layouts.panels",
      futureLayout: "retain",
      panels: [
        {
          futurePanel: 7,
          blocks: [
            {
              futureEntry: "retain",
              block: {
                $type: "place.stream.bio.blocks.text",
                plaintext: "bold",
                futureBlock: [1, 2],
                facets: [
                  {
                    index: { byteStart: 0, byteEnd: 4 },
                    features: [
                      {
                        $type: "place.stream.bio.richtextFacet#bold",
                        futureFeature: true,
                      },
                    ],
                  },
                ],
              },
            },
          ],
        },
      ],
    },
  } as unknown as place.stream.bio.page.Main;
  const before = structuredClone(draft);
  expect(prepareBioForSave(draft, updatedAt)).toEqual({ ...before, updatedAt });
  expect(draft).toEqual(before);
});
