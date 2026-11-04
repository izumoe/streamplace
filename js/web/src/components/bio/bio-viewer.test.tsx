import { BlobRef } from "@atproto/api";
import { renderToStaticMarkup } from "react-dom/server";
import type { place } from "streamplace";
import { afterAll, describe, expect, it, vi } from "vitest";
import { BioViewer } from "./bio-viewer";

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

type Bio = place.stream.bio.page.Main;
type Block = place.stream.bio.layouts.panels.BlockEntry["block"];

function renderBio(
  blocks: Block[],
  description?: Bio["description"],
  options: {
    imagePreviews?: ReadonlyMap<string, string>;
  } = {},
) {
  const container = document.createElement("div");
  container.innerHTML = renderToStaticMarkup(
    <BioViewer
      did="did:plc:creator"
      imagePreviews={options.imagePreviews}
      bio={{
        $type: "place.stream.bio.page",
        createdAt: "2026-10-02T00:00:00Z",
        description,
        layout: {
          $type: "place.stream.bio.layouts.panels",
          panels: [{ blocks: blocks.map((block) => ({ block })) }],
        },
      }}
    />,
  );
  return container;
}

describe("BioViewer", () => {
  it.each([
    ["https://example.com/support", "https://example.com/support"],
    ["mailto:hello@example.com", "mailto:hello@example.com"],
    ["javascript:alert(1)", null],
    ["data:text/html,hello", null],
    [undefined, null],
  ])("links an image only to a safe destination: %s", (url, href) => {
    const container = renderBio([
      {
        $type: "place.stream.bio.blocks.image",
        image: { cid: "image-cid", mimeType: "image/png" },
        aspectRatio: { width: 2, height: 1 },
        alt: "Support my stream",
        url: url as place.stream.bio.blocks.image.Main["url"],
      },
    ]);
    const image = container.querySelector("img");
    expect(image?.alt).toBe("Support my stream");
    expect(image?.closest("a")?.getAttribute("href") ?? null).toBe(href);
  });

  it("allows mailto links in imported blocks and rich text", () => {
    const href = "mailto:creator@example.com?subject=Hello";
    const container = renderBio(
      [
        { $type: "place.stream.bio.blocks.link", url: href, text: "Email me" },
        {
          $type: "place.stream.bio.blocks.socialLinks",
          links: [{ platform: "website", url: href, handle: "Email" }],
        },
      ],
      {
        plaintext: "contact",
        facets: [
          {
            index: { byteStart: 0, byteEnd: 7 },
            features: [
              { $type: "place.stream.bio.richtextFacet#link", uri: href },
            ],
          },
        ],
      },
    );
    expect(
      Array.from(container.querySelectorAll("a")).map((link) =>
        link.getAttribute("href"),
      ),
    ).toEqual([href, href, href]);
  });

  it("uses local previews through nested blocks and the existing CDN for saved images", () => {
    const image = { cid: "draft-cid", mimeType: "image/png" };
    const container = renderBio(
      [
        {
          $type: "place.stream.bio.blocks.orderedList",
          children: [
            {
              content: {
                $type: "place.stream.bio.blocks.text",
                plaintext: "Images",
              },
              unorderedListChildren: {
                children: [
                  {
                    content: {
                      $type: "place.stream.bio.blocks.image",
                      image,
                      alt: "draft",
                      aspectRatio: { width: 1, height: 1 },
                    },
                  },
                ],
              },
            },
          ],
        },
        {
          $type: "place.stream.bio.blocks.link",
          url: "https://example.com/",
          previewImage: image,
        },
        {
          $type: "place.stream.bio.blocks.image",
          image: { cid: "saved-cid", mimeType: "image/png" },
          alt: "saved",
          aspectRatio: { width: 1, height: 1 },
        },
      ],
      undefined,
      {
        imagePreviews: new Map([["draft-cid", "blob:http://localhost/draft"]]),
      },
    );
    expect(
      Array.from(container.querySelectorAll("img")).map((image) =>
        image.getAttribute("src"),
      ),
    ).toEqual([
      "blob:http://localhost/draft",
      "blob:http://localhost/draft",
      "https://cdn.bsky.app/img/feed_fullsize/plain/did%3Aplc%3Acreator/saved-cid@jpeg",
    ]);
  });
  it("preserves Unicode and combines overlapping rich text facets", () => {
    const container = renderBio([], {
      plaintext: "🐱 café link",
      facets: [
        {
          index: { byteStart: 5, byteEnd: 10 },
          features: [{ $type: "place.stream.bio.richtextFacet#bold" }],
        },
        {
          index: { byteStart: 5, byteEnd: 15 },
          features: [{ $type: "place.stream.bio.richtextFacet#italic" }],
        },
        {
          index: { byteStart: 11, byteEnd: 15 },
          features: [
            {
              $type: "app.bsky.richtext.facet#link",
              uri: "https://example.com/",
            },
          ],
        },
      ],
    });
    expect(container.textContent).toBe("🐱 café link");
    expect(container.querySelector("strong em")?.textContent).toBe("café");
    expect(container.querySelector("a")?.getAttribute("href")).toBe(
      "https://example.com/",
    );
  });

  it("ignores invalid byte ranges and escapes HTML without losing text", () => {
    const plaintext = "🐱 <script>alert(1)</script>";
    const container = renderBio([], {
      plaintext,
      facets: [
        {
          index: { byteStart: 1, byteEnd: 4 },
          features: [{ $type: "place.stream.bio.richtextFacet#bold" }],
        },
        {
          index: { byteStart: 0, byteEnd: 999 },
          features: [{ $type: "place.stream.bio.richtextFacet#italic" }],
        },
      ],
    });
    expect(container.textContent).toBe(plaintext);
    expect(container.querySelector("script, strong, em")).toBeNull();
  });

  it("applies every inherited text style and links mentions and tags", () => {
    const container = renderBio([], {
      plaintext: "styled @alice #art",
      facets: [
        {
          index: { byteStart: 0, byteEnd: 6 },
          features: [
            { $type: "place.stream.bio.richtextFacet#bold" },
            { $type: "place.stream.bio.richtextFacet#italic" },
            { $type: "place.stream.bio.richtextFacet#code" },
            { $type: "place.stream.bio.richtextFacet#highlight" },
            { $type: "place.stream.bio.richtextFacet#underline" },
            { $type: "place.stream.bio.richtextFacet#strikethrough" },
          ],
        },
        {
          index: { byteStart: 7, byteEnd: 13 },
          features: [
            {
              $type: "place.stream.bio.richtextFacet#didMention",
              did: "did:plc:alice",
            },
          ],
        },
        {
          index: { byteStart: 14, byteEnd: 18 },
          features: [{ $type: "app.bsky.richtext.facet#tag", tag: "art" }],
        },
      ],
    });
    for (const tag of ["strong", "em", "code", "mark", "u", "s"]) {
      expect(container.querySelector(tag)?.textContent).toBe("styled");
    }
    expect(
      container.querySelector(
        'a[href="https://bsky.app/profile/did%3Aplc%3Aalice"]',
      )?.textContent,
    ).toBe("@alice");
    expect(
      container.querySelector('a[href="https://bsky.app/search?q=%23art"]')
        ?.textContent,
    ).toBe("#art");
  });

  it("never makes executable protocols clickable or embeds untrusted frames", () => {
    const container = renderBio(
      [
        {
          $type: "place.stream.bio.blocks.link",
          url: "javascript:alert(1)",
          text: "unsafe link",
        },
        {
          $type: "place.stream.bio.blocks.embed",
          src: "data:text/html,<script>alert(1)</script>",
          title: "unsafe embed",
        },
        {
          $type: "place.stream.bio.blocks.embed",
          src: "https://example.com/",
          title: "safe preview",
        },
      ],
      {
        plaintext: "unsafe facet",
        facets: [
          {
            index: { byteStart: 0, byteEnd: 12 },
            features: [
              {
                $type: "place.stream.bio.richtextFacet#link",
                uri: "java\nscript:alert(1)",
              },
            ],
          },
        ],
      },
    );
    expect(container.querySelectorAll("a")).toHaveLength(1);
    expect(container.querySelector("a")?.textContent).toContain("safe preview");
    expect(container.querySelector("iframe, script")).toBeNull();
    expect(container.textContent).toContain("unsafe link");
  });

  it("renders nested lists with children precedence and accessible checklists", () => {
    const container = renderBio([
      {
        $type: "place.stream.bio.blocks.unorderedList",
        children: [
          {
            checked: true,
            content: {
              $type: "place.stream.bio.blocks.text",
              plaintext: "done",
            },
            children: [
              {
                content: {
                  $type: "place.stream.bio.blocks.text",
                  plaintext: "nested",
                },
              },
            ],
            orderedListChildren: {
              children: [
                {
                  content: {
                    $type: "place.stream.bio.blocks.text",
                    plaintext: "ignored alternate",
                  },
                },
              ],
            },
          },
        ],
      },
    ]);
    expect(container.querySelectorAll("ul")).toHaveLength(2);
    expect(container.querySelector("input")?.checked).toBe(true);
    expect(container.querySelector("input")?.disabled).toBe(true);
    expect(container.textContent).toContain("nested");
    expect(container.textContent).not.toContain("ignored alternate");
  });

  it("renders alternating ordered and unordered list nesting", () => {
    const container = renderBio([
      {
        $type: "place.stream.bio.blocks.orderedList",
        children: [
          {
            content: {
              $type: "place.stream.bio.blocks.text",
              plaintext: "ordered",
            },
            unorderedListChildren: {
              children: [
                {
                  checked: false,
                  content: {
                    $type: "place.stream.bio.blocks.text",
                    plaintext: "unchecked",
                  },
                  orderedListChildren: {
                    children: [
                      {
                        content: {
                          $type: "place.stream.bio.blocks.text",
                          plaintext: "ordered again",
                        },
                      },
                    ],
                  },
                },
              ],
            },
          },
        ],
      },
    ]);
    expect(container.querySelector("ol > li > ul > li > ol")?.textContent).toBe(
      "ordered again",
    );
    expect(container.querySelector("input")?.checked).toBe(false);
    expect(container.textContent).toContain("unchecked");
  });

  it("renders schedule, headings, quotes, socials and record references", () => {
    const container = renderBio([
      {
        $type: "place.stream.bio.blocks.header",
        plaintext: "About me",
        level: 2,
      },
      { $type: "place.stream.bio.blocks.blockquote", plaintext: "a quote" },
      { $type: "place.stream.bio.blocks.divider" },
      {
        $type: "place.stream.bio.blocks.schedule",
        timezone: "America/Chicago",
        slots: [
          {
            dayOfWeek: "mon",
            startTime: "18:00",
            endTime: "20:00",
            title: "art",
          },
        ],
      },
      {
        $type: "place.stream.bio.blocks.socialLinks",
        links: [
          { platform: "bluesky", url: "https://bsky.app/profile/alice.test" },
        ],
      },
      {
        $type: "place.stream.bio.blocks.bskyPost",
        post: {
          uri: "at://did:plc:creator/app.bsky.feed.post/123",
          cid: "cid",
        },
      },
      {
        $type: "place.stream.bio.blocks.livestream",
        livestream: {
          uri: "at://did:plc:creator/place.stream.livestream/456",
          cid: "cid",
        },
      },
    ]);
    expect(container.querySelector("h3")?.textContent).toBe("About me");
    expect(container.querySelector("blockquote")?.textContent).toBe("a quote");
    expect(container.querySelector("hr")).not.toBeNull();
    expect(container.textContent).toContain("America/Chicago");
    expect(container.textContent).toContain("18:00 - 20:00 art");
    expect(
      container.querySelector(
        'a[href="https://bsky.app/profile/did%3Aplc%3Acreator/post/123"]',
      ),
    ).not.toBeNull();
    expect(
      container.querySelector('a[href="/did%3Aplc%3Acreator"]'),
    ).not.toBeNull();
  });

  it("renders legacy blob images with alt text and ignores invalid aspect ratios", () => {
    const container = renderBio([
      {
        $type: "place.stream.bio.blocks.image",
        image: {
          cid: "bafkreigh2akiscaildcgjd5thb4cc2pmrm3g4jcj3rkdxlixjxznyjivnm",
          mimeType: "image/jpeg",
        },
        alt: "a cat",
        aspectRatio: { width: 0, height: 0 },
      },
    ]);
    const image = container.querySelector("img");
    expect(image?.getAttribute("src")).toBe(
      "https://cdn.bsky.app/img/feed_fullsize/plain/did%3Aplc%3Acreator/bafkreigh2akiscaildcgjd5thb4cc2pmrm3g4jcj3rkdxlixjxznyjivnm@jpeg",
    );
    expect(image?.alt).toBe("a cat");
    expect(image?.style.aspectRatio).toBe("");
  });

  it("renders current blob refs and their declared aspect ratio", () => {
    const blob = BlobRef.fromJsonRef({
      cid: "bafkreigh2akiscaildcgjd5thb4cc2pmrm3g4jcj3rkdxlixjxznyjivnm",
      mimeType: "image/jpeg",
    });
    const container = renderBio([
      {
        $type: "place.stream.bio.blocks.image",
        image: {
          $type: "blob",
          ref: blob.ref,
          mimeType: blob.mimeType,
          size: 64,
        },
        alt: "a landscape",
        aspectRatio: { width: 4, height: 3 },
      },
    ]);
    const image = container.querySelector("img");
    expect(image?.getAttribute("src")).toBe(
      `https://cdn.bsky.app/img/feed_fullsize/plain/did%3Aplc%3Acreator/${blob.ref.toString()}@jpeg`,
    );
    expect(image?.style.aspectRatio).toBe("4 / 3");
    expect(image?.alt).toBe("a landscape");
  });
});

describe("untrusted bio facet overlap", () => {
  it.each([100, 1500])(
    "deduplicates overlapping formatting for %i facets",
    (count) => {
      const plaintext = "x".repeat(count * 2);
      const container = renderBio([], {
        plaintext,
        facets: Array.from({ length: count }, (_, index) => ({
          index: { byteStart: index, byteEnd: plaintext.length },
          features: [{ $type: "place.stream.bio.richtextFacet#bold" }],
        })),
      });
      expect(container.textContent).toBe(plaintext);
      expect(container.querySelectorAll("strong")).toHaveLength(count);
      expect(container.querySelector("strong strong")).toBeNull();
    },
  );
});

it("restores a lower priority link when an earlier annotation ends", () => {
  const container = renderBio([], {
    plaintext: "abcdef",
    facets: [
      {
        index: { byteStart: 1, byteEnd: 3 },
        features: [
          {
            $type: "place.stream.bio.richtextFacet#link",
            uri: "javascript:alert(1)",
          },
          {
            $type: "place.stream.bio.richtextFacet#link",
            uri: "https://first.test/",
          },
        ],
      },
      {
        index: { byteStart: 0, byteEnd: 6 },
        features: [
          {
            $type: "app.bsky.richtext.facet#link",
            uri: "https://second.test/",
          },
        ],
      },
    ],
  });
  expect(
    Array.from(container.querySelectorAll("a")).map((link) => ({
      href: link.href,
      text: link.textContent,
    })),
  ).toEqual([
    { href: "https://second.test/", text: "a" },
    { href: "https://first.test/", text: "bc" },
    { href: "https://second.test/", text: "def" },
  ]);
});
