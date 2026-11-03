export function requireBioImageFile(
  file: { type: string; size: number },
  message = "Choose an image smaller than 1 MB.",
): void {
  if (
    !file.type.startsWith("image/") ||
    file.size <= 0 ||
    file.size > 1_000_000
  )
    throw new Error(message);
}

export function getBioImageUrl(did: string, cid: string): string {
  return `https://cdn.bsky.app/img/feed_fullsize/plain/${encodeURIComponent(did)}/${encodeURIComponent(cid)}@jpeg`;
}
