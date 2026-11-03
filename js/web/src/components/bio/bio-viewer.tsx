import { useBioImage } from "@/hooks/use-bio-image";
import { getBioImageUrl, getBioLinkUrl } from "@streamplace/core";
import { Fragment, useState, type ReactNode } from "react";
import { place } from "streamplace";
import { Card } from "../ui/card";
import { BioPanelsLayout } from "./bio-panels-layout";

type Bio = place.stream.bio.page.Main;
type Block = place.stream.bio.layouts.panels.BlockEntry["block"];
type Facet = place.stream.bio.richtextFacet.Main;

const linkClass =
  "text-primary underline underline-offset-4 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring";

function BioLink({ href, children }: { href?: string; children: ReactNode }) {
  if (!href) return <>{children}</>;
  return (
    <a href={href} className={linkClass} rel="noopener noreferrer">
      {children}
    </a>
  );
}

type ImageSources = {
  did?: string;
  imagePreviews?: ReadonlyMap<string, string>;
};

export function BioViewer({
  bio,
  did,
  imagePreviews,
}: {
  bio: Bio;
  did?: string;
  imagePreviews?: ReadonlyMap<string, string>;
}) {
  const imageSources: ImageSources = {
    did,
    imagePreviews,
  };
  const layout = bio.layout;
  return (
    <div className="@container space-y-6 break-words">
      {bio.description && (
        <p className="leading-relaxed whitespace-pre-wrap">
          <RichText {...bio.description} />
        </p>
      )}
      {bio.socials && <SocialLinks links={bio.socials} />}
      {layout && place.stream.bio.layouts.panels.main.isTypeOf(layout) && (
        <BioPanels panels={layout.panels} sources={imageSources} />
      )}
    </div>
  );
}

function BioPanels({
  panels,
  sources,
}: {
  panels: place.stream.bio.layouts.panels.Panel[];
  sources: ImageSources;
}) {
  return (
    <BioPanelsLayout role="list">
      {panels.map((panel, panelIndex) => (
        <Card
          key={panelIndex}
          role="listitem"
          data-testid={`bio-profile-card-${panelIndex}`}
          className="mb-4 w-(--bio-panel-width) max-w-full min-w-0 space-y-4 px-4 py-3"
        >
          {panel.blocks.map((entry, blockIndex) => (
            <div
              key={blockIndex}
              className={
                entry.alignment === "center"
                  ? "text-center"
                  : entry.alignment === "right"
                    ? "text-right"
                    : "text-left"
              }
            >
              <BioBlockView block={entry.block} {...sources} />
            </div>
          ))}
        </Card>
      ))}
    </BioPanelsLayout>
  );
}

function SocialLinks({ links }: { links: place.stream.bio.defs.Social[] }) {
  return (
    <div className="flex flex-wrap gap-3">
      {links.map((link, index) => (
        <BioLink key={index} href={getBioLinkUrl(link.url)}>
          {link.handle || link.platform}
        </BioLink>
      ))}
    </div>
  );
}

function BioImage({
  image,
  alt = "",
  aspectRatio,
  sources,
}: {
  sources: ImageSources;
  image: place.stream.bio.blocks.image.Main["image"];
  alt?: string;
  aspectRatio?: place.stream.bio.blocks.image.AspectRatio;
}) {
  const cid = "ref" in image ? image.ref.toString() : image.cid;
  const src =
    sources.imagePreviews?.get(cid) ??
    (sources.did ? getBioImageUrl(sources.did, cid) : undefined);
  const [failedSource, setFailedSource] = useState<string>();
  if (!src) return alt ? <p>{alt}</p> : null;
  const ratio =
    aspectRatio && aspectRatio.width > 0 && aspectRatio.height > 0
      ? `${aspectRatio.width} / ${aspectRatio.height}`
      : undefined;
  if (failedSource === src && sources.did)
    return (
      <BioPDSImage
        key={src}
        did={sources.did}
        cid={cid}
        alt={alt}
        ratio={ratio}
      />
    );
  return (
    <img
      src={src}
      onError={() => setFailedSource(src)}
      alt={alt}
      loading="lazy"
      className="h-auto w-full rounded-lg object-contain"
      style={{ aspectRatio: ratio }}
    />
  );
}

function BioPDSImage({
  did,
  cid,
  alt,
  ratio,
}: {
  did: string;
  cid: string;
  alt: string;
  ratio?: string;
}) {
  const { src, isError } = useBioImage(did, cid);
  const [failedSource, setFailedSource] = useState<string>();
  if (isError || (src && failedSource === src))
    return alt ? <p>{alt}</p> : null;
  if (!src) return null;
  return (
    <img
      src={src}
      alt={alt}
      loading="lazy"
      onError={() => setFailedSource(src)}
      className="h-auto w-full rounded-lg object-contain"
      style={{ aspectRatio: ratio }}
    />
  );
}

export function BioBlockView({
  block,
  did,
  imagePreviews,
}: { block: Block } & ImageSources) {
  const sources = { did, imagePreviews };
  const blocks = place.stream.bio.blocks;
  if (blocks.text.main.isTypeOf(block)) {
    return (
      <p
        className={`leading-relaxed whitespace-pre-wrap ${block.textSize === "small" ? "text-sm" : block.textSize === "large" ? "text-lg" : "text-base"}`}
      >
        <RichText {...block} />
      </p>
    );
  }
  if (blocks.header.main.isTypeOf(block)) {
    const Heading = block.level === 3 ? "h4" : block.level === 2 ? "h3" : "h2";
    return (
      <Heading className="text-lg font-semibold whitespace-pre-wrap">
        <RichText {...block} />
      </Heading>
    );
  }
  if (blocks.blockquote.main.isTypeOf(block)) {
    return (
      <blockquote className="border-border text-muted-foreground border-l pl-4 whitespace-pre-wrap italic">
        <RichText {...block} />
      </blockquote>
    );
  }
  if (blocks.divider.main.isTypeOf(block))
    return <hr className="border-border" />;
  if (blocks.image.main.isTypeOf(block))
    return (
      <BioLink href={getBioLinkUrl(block.url)}>
        <BioImage {...block} sources={sources} />
      </BioLink>
    );
  if (blocks.orderedList.main.isTypeOf(block)) {
    return <BioList items={block.children} ordered sources={sources} />;
  }
  if (blocks.unorderedList.main.isTypeOf(block)) {
    return <BioList items={block.children} sources={sources} />;
  }
  if (blocks.socialLinks.main.isTypeOf(block)) {
    return <SocialLinks links={block.links} />;
  }
  if (blocks.link.main.isTypeOf(block) || blocks.embed.main.isTypeOf(block)) {
    const url = "url" in block ? block.url : block.src;
    const title = "url" in block ? block.text : block.title;
    return (
      <div className="border-border space-y-2 rounded-lg border p-4">
        {block.previewImage && (
          <BioImage image={block.previewImage} sources={sources} />
        )}
        <BioLink href={getBioLinkUrl(url)}>{title || url}</BioLink>
        {block.description && (
          <p className="text-muted-foreground text-sm">{block.description}</p>
        )}
      </div>
    );
  }
  if (blocks.schedule.main.isTypeOf(block)) {
    return (
      <div className="space-y-2">
        <p className="text-muted-foreground text-sm">{block.timezone}</p>
        <ul className="space-y-2">
          {block.slots.map((slot, index) => (
            <li key={index} className="flex flex-wrap gap-2">
              <span className="font-medium">{slot.dayOfWeek}</span>
              <span className="tabular-nums">
                {slot.startTime}
                {slot.endTime ? ` - ${slot.endTime}` : ""}
                {slot.title ? ` ${slot.title}` : ""}
              </span>
            </li>
          ))}
        </ul>
      </div>
    );
  }
  if (
    blocks.bskyPost.main.isTypeOf(block) ||
    blocks.livestream.main.isTypeOf(block)
  ) {
    const isPost = blocks.bskyPost.main.isTypeOf(block);
    const uri = isPost ? block.post.uri : block.livestream.uri;
    const match = /^at:\/\/([^/]+)\/([^/]+)\/([^/]+)$/.exec(uri);
    const href =
      match &&
      match[2] === (isPost ? "app.bsky.feed.post" : "place.stream.livestream")
        ? isPost
          ? `https://bsky.app/profile/${encodeURIComponent(match[1])}/post/${encodeURIComponent(match[3])}`
          : `/${encodeURIComponent(match[1])}`
        : undefined;
    return (
      <BioLink href={href}>{isPost ? "Bluesky post" : "Livestream"}</BioLink>
    );
  }
  return null;
}

function BioList({
  items,
  ordered = false,
  sources,
}: {
  sources: ImageSources;
  items: (
    | place.stream.bio.blocks.orderedList.ListItem
    | place.stream.bio.blocks.unorderedList.ListItem
  )[];
  ordered?: boolean;
}) {
  const List = ordered ? "ol" : "ul";
  return (
    <List
      className={`space-y-2 pl-6 ${ordered ? "list-decimal" : "list-disc"}`}
    >
      {items.map((item, index) => (
        <li key={index} className={"checked" in item ? "list-none" : undefined}>
          {"checked" in item && (
            <input
              type="checkbox"
              checked={item.checked}
              disabled
              aria-label="Checklist item"
              className="mr-2"
            />
          )}
          <BioBlockView block={item.content} {...sources} />
          {item.children ? (
            <BioList
              items={item.children}
              ordered={ordered}
              sources={sources}
            />
          ) : "unorderedListChildren" in item && item.unorderedListChildren ? (
            <BioList
              items={item.unorderedListChildren.children}
              sources={sources}
            />
          ) : "orderedListChildren" in item && item.orderedListChildren ? (
            <BioList
              items={item.orderedListChildren.children}
              ordered
              sources={sources}
            />
          ) : null}
        </li>
      ))}
    </List>
  );
}

const formattingTypes = new Set([
  "place.stream.bio.richtextFacet#bold",
  "place.stream.bio.richtextFacet#italic",
  "place.stream.bio.richtextFacet#code",
  "place.stream.bio.richtextFacet#highlight",
  "place.stream.bio.richtextFacet#underline",
  "place.stream.bio.richtextFacet#strikethrough",
]);

function RichText({
  plaintext,
  facets,
}: {
  plaintext: string;
  facets?: Facet[];
}) {
  if (!facets?.length) return <>{plaintext}</>;
  const byteToChar = new Map<number, number>([[0, 0]]);
  let byte = 0;
  let char = 0;
  for (const codepoint of plaintext) {
    const value = codepoint.codePointAt(0)!;
    byte += value < 0x80 ? 1 : value < 0x800 ? 2 : value < 0x10000 ? 3 : 4;
    char += codepoint.length;
    byteToChar.set(byte, char);
  }
  const annotations: { style?: string; href?: string }[] = [];
  const events = new Map<number, { id: number; add: boolean }[]>();
  for (const facet of facets) {
    const { byteStart, byteEnd } = facet.index;
    if (
      byteStart >= byteEnd ||
      !byteToChar.has(byteStart) ||
      !byteToChar.has(byteEnd)
    )
      continue;
    for (const feature of facet.features) {
      const style = formattingTypes.has(feature.$type)
        ? feature.$type
        : undefined;
      let href: string | undefined;
      switch (feature.$type) {
        case "place.stream.bio.richtextFacet#link":
        case "app.bsky.richtext.facet#link":
          if ("uri" in feature && typeof feature.uri === "string")
            href = getBioLinkUrl(feature.uri);
          break;
        case "place.stream.bio.richtextFacet#atMention":
          if ("href" in feature && typeof feature.href === "string")
            href = getBioLinkUrl(feature.href);
          break;
        case "place.stream.bio.richtextFacet#didMention":
        case "app.bsky.richtext.facet#mention":
          if (
            "did" in feature &&
            typeof feature.did === "string" &&
            feature.did.startsWith("did:")
          )
            href = `https://bsky.app/profile/${encodeURIComponent(feature.did)}`;
          break;
        case "app.bsky.richtext.facet#tag":
          if ("tag" in feature && typeof feature.tag === "string")
            href = `https://bsky.app/search?q=${encodeURIComponent(`#${feature.tag}`)}`;
          break;
      }
      if (!style && !href) continue;
      const id = annotations.push({ style, href }) - 1;
      for (const [position, add] of [
        [byteStart, true],
        [byteEnd, false],
      ] as const) {
        const entries = events.get(position) ?? [];
        entries.push({ id, add });
        events.set(position, entries);
      }
    }
  }
  const boundaries = [...new Set([0, byte, ...events.keys()])].sort(
    (a, b) => a - b,
  );
  const styles = new Map<string, number>();
  const links: number[] = [];
  return boundaries.slice(0, -1).map((start, index) => {
    for (const { id, add } of events.get(start) ?? []) {
      const { style, href } = annotations[id];
      if (style) styles.set(style, (styles.get(style) ?? 0) + (add ? 1 : -1));
      if (href) {
        // IDs retain source priority even when an earlier link starts later.
        let low = 0;
        let high = links.length;
        while (low < high) {
          const middle = (low + high) >>> 1;
          if (links[middle] < id) low = middle + 1;
          else high = middle;
        }
        if (add) links.splice(low, 0, id);
        else links.splice(low, 1);
      }
    }
    let content: ReactNode = plaintext.slice(
      byteToChar.get(start),
      byteToChar.get(boundaries[index + 1]),
    );
    for (const type of Array.from(styles.keys()).reverse()) {
      if (!styles.get(type)) continue;
      switch (type) {
        case "place.stream.bio.richtextFacet#bold":
          content = <strong>{content}</strong>;
          break;
        case "place.stream.bio.richtextFacet#italic":
          content = <em>{content}</em>;
          break;
        case "place.stream.bio.richtextFacet#code":
          content = <code>{content}</code>;
          break;
        case "place.stream.bio.richtextFacet#highlight":
          content = (
            <mark className="bg-accent text-accent-foreground">{content}</mark>
          );
          break;
        case "place.stream.bio.richtextFacet#underline":
          content = <u>{content}</u>;
          break;
        case "place.stream.bio.richtextFacet#strikethrough":
          content = <s>{content}</s>;
          break;
      }
    }
    return (
      <Fragment key={start}>
        <BioLink href={links.length ? annotations[links[0]].href : undefined}>
          {content}
        </BioLink>
      </Fragment>
    );
  });
}
