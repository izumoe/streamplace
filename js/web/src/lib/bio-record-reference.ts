import type { com } from "streamplace";

export type BioRecordCollection =
  "app.bsky.feed.post" | "place.stream.livestream";
export type BioRecordSource = com.atproto.repo.getRecord.$Params;
export type BioRecordReference = com.atproto.repo.strongRef.Main;
export type ResolveBioRecord = (
  source: BioRecordSource,
) => Promise<BioRecordReference>;

export function parseBioRecordSource(
  source: string,
  collection: BioRecordCollection,
): BioRecordSource {
  const trimmed = source.trim();
  let match = trimmed.match(
    /^at:\/\/([^/?#]+)\/([^/?#]+)\/([A-Za-z0-9._~:-]+)$/,
  );
  if (!match) {
    const url = new URL(trimmed);
    if (
      collection !== "app.bsky.feed.post" ||
      url.origin !== "https://bsky.app" ||
      url.username ||
      url.password
    ) {
      throw new Error("Use a Bluesky post URL or an AT URI for this record.");
    }
    const path = url.pathname.match(
      /^\/profile\/([^/]+)\/post\/([A-Za-z0-9._~:-]+)$/,
    );
    if (path)
      match = [
        trimmed,
        decodeURIComponent(path[1]),
        "app.bsky.feed.post",
        path[2],
      ];
  }
  if (
    !match ||
    match[2] !== collection ||
    match[3] === "." ||
    match[3] === ".."
  ) {
    throw new Error("This link does not point to the right kind of record.");
  }
  return {
    repo: match[1] as BioRecordSource["repo"],
    collection,
    rkey: match[3],
  };
}
