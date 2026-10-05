import type { RequestHeaders } from "./request-headers.mjs";

const tagsKey = Symbol.for("ocel.next.origin-cache-tags.v1");

export function collectTags(headers: RequestHeaders): void {
  headers[tagsKey] = [];
}

export function notedTags(headers: RequestHeaders): string[] {
  const noted = headers[tagsKey];
  return Array.isArray(noted) ? noted : [];
}

export function noteTags(headers: RequestHeaders, tags: readonly string[]): void {
  const noted = headers[tagsKey];
  if (!Array.isArray(noted)) return;
  noted.length = 0;
  noted.push(...tags);
}
