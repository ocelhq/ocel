const tagsKey = Symbol.for("ocel.next.origin-cache-tags.v1");

type Headers = Record<string | symbol, any>;

export function collectTags(headers: Headers): void {
  headers[tagsKey] = [];
}

export function notedTags(headers: Headers): string[] {
  const noted = headers[tagsKey];
  return Array.isArray(noted) ? noted : [];
}

export function noteTags(headers: Headers, tags: readonly string[]): void {
  const noted = headers[tagsKey];
  if (!Array.isArray(noted)) return;
  noted.length = 0;
  noted.push(...tags);
}
