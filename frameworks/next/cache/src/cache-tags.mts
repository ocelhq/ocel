const maxTagBytes = 1024;
const maxTags = 1000;
const maxTagsBytes = 16 * 1024;

const encoder = new TextEncoder();

export interface BoundCacheTags {
  tags: string[];
  dropped: number;
}

function sanitize(tag: string): string {
  return tag.replace(/[\t ]/g, (c) => encodeURIComponent(c));
}

export function boundCacheTags(raw: readonly string[]): BoundCacheTags {
  const tags: string[] = [];
  const seen = new Set<string>();
  let dropped = 0;
  let bytes = 0;
  let full = false;

  for (const part of raw) {
    const tag = sanitize(part);
    if (seen.has(tag)) continue;
    const size = encoder.encode(tag).length;
    if (!tag || size > maxTagBytes) {
      dropped++;
      continue;
    }
    const cost = size + (tags.length > 0 ? 1 : 0);
    if (full || tags.length >= maxTags || bytes + cost > maxTagsBytes) {
      full = true;
      dropped++;
      continue;
    }
    bytes += cost;
    seen.add(tag);
    tags.push(tag);
  }

  return { tags, dropped };
}
