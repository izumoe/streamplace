import { describe, expect, it } from "vitest";
import { parseBioRecordSource } from "./bio-record-reference";

describe("bio record references", () => {
  it("accepts a Bluesky post URL with a handle or DID", () => {
    expect(
      parseBioRecordSource(
        "https://bsky.app/profile/alice.test/post/abc123",
        "app.bsky.feed.post",
      ),
    ).toEqual({
      repo: "alice.test",
      collection: "app.bsky.feed.post",
      rkey: "abc123",
    });
    expect(
      parseBioRecordSource(
        " https://bsky.app/profile/did:plc:creator/post/abc123?ref=share ",
        "app.bsky.feed.post",
      ),
    ).toEqual({
      repo: "did:plc:creator",
      collection: "app.bsky.feed.post",
      rkey: "abc123",
    });
  });

  it("accepts AT URIs for posts and livestreams", () => {
    expect(
      parseBioRecordSource(
        "at://did:plc:creator/app.bsky.feed.post/abc123",
        "app.bsky.feed.post",
      ),
    ).toEqual({
      repo: "did:plc:creator",
      collection: "app.bsky.feed.post",
      rkey: "abc123",
    });
    expect(
      parseBioRecordSource(
        "at://alice.test/place.stream.livestream/live123",
        "place.stream.livestream",
      ),
    ).toEqual({
      repo: "alice.test",
      collection: "place.stream.livestream",
      rkey: "live123",
    });
  });

  it.each([
    "at://did:plc:creator/place.stream.livestream/abc123",
    "at://did:plc:creator/app.bsky.feed.post/",
    "at://did:plc:creator/app.bsky.feed.post/abc123/extra",
    "at://did:plc:creator/app.bsky.feed.post/abc123?other=record",
    "https://example.com/profile/alice.test/post/abc123",
    "https://bsky.app.example.com/profile/alice.test/post/abc123",
    "https://alice.test@bsky.app/profile/alice.test/post/abc123",
    "http://bsky.app/profile/alice.test/post/abc123",
    "https://bsky.app/profile/alice.test/post/",
    "https://bsky.app/profile/alice.test/post/abc123/extra",
    "https://bsky.app/profile//post/abc123",
    "",
    "   ",
  ])("rejects invalid or unrelated post sources: %s", (source) => {
    expect(() => parseBioRecordSource(source, "app.bsky.feed.post")).toThrow();
  });

  it("rejects a post when adding a livestream", () => {
    expect(() =>
      parseBioRecordSource(
        "at://did:plc:creator/app.bsky.feed.post/abc123",
        "place.stream.livestream",
      ),
    ).toThrow();
    expect(() =>
      parseBioRecordSource(
        "https://bsky.app/profile/alice.test/post/abc123",
        "place.stream.livestream",
      ),
    ).toThrow();
  });
});
