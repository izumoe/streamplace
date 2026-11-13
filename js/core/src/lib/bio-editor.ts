import { place } from "streamplace";
import { getBioLinkUrl } from "./bio-document";

export function setBioPlaintext<
  T extends {
    plaintext: string;
    facets?: place.stream.bio.richtextFacet.Main[];
  },
>(value: T, plaintext: string): T {
  if (plaintext === value.plaintext) return value;
  const { facets, ...rest } = value;
  return { ...rest, plaintext } as T;
}

export function moveBioItem<T>(items: T[], from: number, to: number): T[] {
  if (
    from === to ||
    from < 0 ||
    from >= items.length ||
    to < 0 ||
    to >= items.length
  )
    return items;
  const next = [...items];
  next.splice(to, 0, next.splice(from, 1)[0]);
  return next;
}

export function prepareBioForSave(
  draft: place.stream.bio.page.Main,
  updatedAt: string,
): place.stream.bio.page.Main {
  const record = { ...draft, updatedAt: updatedAt as typeof draft.createdAt };
  if (
    record.layout &&
    place.stream.bio.layouts.panels.main.isTypeOf(record.layout) &&
    !record.layout.panels.length
  ) {
    delete record.layout;
  }
  place.stream.bio.page.$parse(record);
  const requireUrl = (value: string, allowMailto: boolean) => {
    const url = getBioLinkUrl(value);
    if (!url || (!allowMailto && url.startsWith("mailto:")))
      throw new Error(
        allowMailto
          ? "Links must use HTTP or HTTPS URLs, or mailto links."
          : "Embeds must use HTTP or HTTPS URLs.",
      );
  };
  record.socials?.forEach((social) => requireUrl(social.url, true));
  const requireBlockLinks = (
    block: place.stream.bio.layouts.panels.BlockEntry["block"],
  ) => {
    if (place.stream.bio.blocks.link.main.isTypeOf(block))
      requireUrl(block.url, true);
    if (place.stream.bio.blocks.image.main.isTypeOf(block) && block.url)
      requireUrl(block.url, true);
    if (place.stream.bio.blocks.embed.main.isTypeOf(block))
      requireUrl(block.src, false);
    if (place.stream.bio.blocks.socialLinks.main.isTypeOf(block))
      block.links.forEach((social) => requireUrl(social.url, true));
    if (
      place.stream.bio.blocks.orderedList.main.isTypeOf(block) ||
      place.stream.bio.blocks.unorderedList.main.isTypeOf(block)
    )
      block.children.forEach(requireItemLinks);
  };
  const requireItemLinks = (
    item:
      | place.stream.bio.blocks.orderedList.ListItem
      | place.stream.bio.blocks.unorderedList.ListItem,
  ) => {
    requireBlockLinks(item.content);
    item.children?.forEach(requireItemLinks);
    if ("orderedListChildren" in item)
      item.orderedListChildren?.children.forEach(requireItemLinks);
    if ("unorderedListChildren" in item)
      item.unorderedListChildren?.children.forEach(requireItemLinks);
  };
  if (
    record.layout &&
    place.stream.bio.layouts.panels.main.isTypeOf(record.layout)
  ) {
    for (const panel of record.layout.panels) {
      for (const { block } of panel.blocks) requireBlockLinks(block);
    }
  }
  return record;
}
