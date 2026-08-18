const maxTagBytes = 1024;
const maxTags = 1000;
const maxTagsBytes = 16 * 1024;

const encoder = new TextEncoder();

const softTagPrefix = "_N_T_/";

const releaseSeparator = "|";

const maxStoredTagLength = 256;

export interface BoundCacheTags {
  tags: string[];
  dropped: number;
}

function percentEncode(ch: string): string {
  let out = "";
  for (const byte of encoder.encode(ch)) {
    out += "%" + byte.toString(16).toUpperCase().padStart(2, "0");
  }
  return out;
}

function transmittable(ch: string): boolean {
  const code = ch.codePointAt(0)!;
  return code >= 33 && code <= 126 && ch !== "," && ch !== "%";
}

function sanitize(tag: string): string {
  let out = "";
  for (const ch of tag) out += transmittable(ch) ? ch : percentEncode(ch);
  return out;
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

function soft(tag: string): boolean {
  return tag.startsWith(softTagPrefix);
}

export interface StoredCacheTags {
  tags: string[];
  unstorable: string[];
  overflowed: string[];
}

export function storedCacheTags(
  release: string,
  raw: readonly string[],
  limit = Number.POSITIVE_INFINITY,
): StoredCacheTags {
  const ordered = [...raw]
    .filter((tag) => tag !== "")
    .sort((a, b) => Number(soft(b)) - Number(soft(a)));

  const tags: string[] = [];
  const unstorable: string[] = [];
  const overflowed: string[] = [];
  const seen = new Set<string>();

  for (const tag of ordered) {
    const stamped = release + releaseSeparator + tag;
    if (stamped.length > maxStoredTagLength || sanitize(tag) !== tag) {
      unstorable.push(tag);
      continue;
    }
    if (seen.has(stamped)) continue;
    seen.add(stamped);
    if (tags.length >= limit) {
      overflowed.push(tag);
      continue;
    }
    tags.push(stamped);
  }

  return { tags, unstorable, overflowed };
}
