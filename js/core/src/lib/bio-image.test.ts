import { describe, expect, it } from "vitest";
import { getBioImageUrl, requireBioImageFile } from "./bio-image";

describe("profile image uploads", () => {
  it.each([1, 1_000_000])("accepts an image of %i bytes", (size) => {
    expect(() =>
      requireBioImageFile({ type: "image/png", size }),
    ).not.toThrow();
  });
  it.each([
    { type: "image/png", size: 0 },
    { type: "image/png", size: 1_000_001 },
    { type: "application/pdf", size: 100 },
    { type: "", size: 100 },
  ])("rejects empty, oversized, and non-image files: %j", (file) => {
    expect(() => requireBioImageFile(file)).toThrow(/image.*1 MB/);
  });
});

describe("getBioImageUrl", () => {
  it("builds the public image CDN URL", () => {
    expect(getBioImageUrl("did:plc:creator", "bafkreicid")).toBe(
      "https://cdn.bsky.app/img/feed_fullsize/plain/did%3Aplc%3Acreator/bafkreicid@jpeg",
    );
  });

  it("encodes each identifier as one path segment", () => {
    expect(
      getBioImageUrl("did:web:example.com/user#profile", "cid/path?x=y"),
    ).toBe(
      "https://cdn.bsky.app/img/feed_fullsize/plain/did%3Aweb%3Aexample.com%2Fuser%23profile/cid%2Fpath%3Fx%3Dy@jpeg",
    );
  });
});
