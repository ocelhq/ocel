export type Stamp = { cached: string; live: string };

export type PostedForm = {
  action: string;
  method: string;
  fields: Record<string, string>;
};

const ATTRIBUTE = /([a-zA-Z0-9_$:.-]+)="([^"]*)"/g;
const COMMENT = /<!--[\s\S]*?-->/g;

function escaped(text: string): string {
  return text.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

function attributes(tag: string): Record<string, string> {
  const found: Record<string, string> = {};
  for (const [, name, value] of tag.matchAll(ATTRIBUTE)) {
    found[name!] = decodeEntities(value!);
  }
  return found;
}

function decodeEntities(text: string): string {
  return text
    .replace(/&quot;/g, '"')
    .replace(/&#x27;/g, "'")
    .replace(/&#39;/g, "'")
    .replace(/&lt;/g, "<")
    .replace(/&gt;/g, ">")
    .replace(/&amp;/g, "&");
}

export function markerOrNone(html: string, name: string): string | undefined {
  const found = new RegExp(`data-ocel="${escaped(name)}"[^>]*>([\\s\\S]*?)</`).exec(html);
  if (!found) {
    return undefined;
  }
  return decodeEntities(found[1]!.replace(COMMENT, "")).trim();
}

export function marker(html: string, name: string): string {
  const found = markerOrNone(html, name);
  if (found === undefined) {
    throw new Error(`no element carried data-ocel="${name}"`);
  }
  return found;
}

export function stamp(html: string, scope: string): Stamp {
  return { cached: marker(html, `${scope}:cached`), live: marker(html, `${scope}:live`) };
}

export function assetPath(html: string): string {
  const found = /["'](\/_next\/static\/[^"']+)["']/.exec(html);
  if (!found) {
    throw new Error("the page linked no hashed static asset");
  }
  return decodeEntities(found[1]!);
}

export function form(html: string): PostedForm {
  const found = /<form\b([^>]*)>([\s\S]*?)<\/form>/.exec(html);
  if (!found) {
    throw new Error("the page rendered no form");
  }
  const attrs = attributes(found[1]!);
  const fields: Record<string, string> = {};
  for (const [, tag] of found[2]!.matchAll(/<input\b([^>]*)>/g)) {
    const input = attributes(tag!);
    if (input.type === "hidden" && input.name !== undefined) {
      fields[input.name] = input.value ?? "";
    }
  }
  return {
    action: attrs.action ?? "",
    method: (attrs.method ?? "get").toLowerCase(),
    fields,
  };
}

export function firstChunkWith(chunks: string[], sentinel: string): number {
  const at = chunks.findIndex((chunk) => chunk.includes(sentinel));
  if (at === -1) {
    throw new Error(`no chunk carried ${sentinel}`);
  }
  return at;
}

export async function chunksOf(res: Response): Promise<string[]> {
  const body = res.body;
  if (!body) {
    throw new Error("the response carried no body to read chunk by chunk");
  }
  const decoder = new TextDecoder();
  const chunks: string[] = [];
  for await (const piece of body as unknown as AsyncIterable<Uint8Array>) {
    chunks.push(decoder.decode(piece, { stream: true }));
  }
  chunks.push(decoder.decode());
  return chunks.filter((chunk) => chunk.length > 0);
}
