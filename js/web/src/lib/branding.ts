export function isWebUrl(value: string): boolean {
  try {
    const url = new URL(value);
    return url.protocol === "https:" || url.protocol === "http:";
  } catch {
    return false;
  }
}

export function brandingLinks(data?: string): { label: string; url: string }[] {
  try {
    const links: unknown = JSON.parse(data ?? "[]");
    if (!Array.isArray(links)) return [];
    return links.flatMap((link) => {
      const label = link?.label ?? link?.text;
      return typeof label === "string" &&
        label.trim() &&
        typeof link?.url === "string" &&
        isWebUrl(link.url)
        ? [{ label, url: link.url }]
        : [];
    });
  } catch {
    return [];
  }
}
