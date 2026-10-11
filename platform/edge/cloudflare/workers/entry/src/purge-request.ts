import { storedCacheTags } from "@framework/next-cache";
import type { Env } from "./env";
import type { PurgeMode, RaisedTag } from "./purge-backlog";

export const PURGE_PATH = "/.ocel/purge";
const PURGE_QUEUE_NAME = "purge";
export const PURGE_SIGNATURE_HEADER = "x-ocel-signature";

export const MAX_TAGS_PER_REQUEST = 1000;
const MAX_BODY_BYTES = 512 * 1024;

export type ServedLookup =
  | { kind: "record"; record: { purgeSecret?: string } }
  | { kind: "not-served" };

export interface PurgeDeps {
  readServed(release: string): Promise<ServedLookup>;
  queue: { raise(mode: PurgeMode, tags: RaisedTag[]): Promise<void> };
  warn?(message: string): void;
}

interface PurgeBody {
  release: string;
  mode: PurgeMode;
  tags: RaisedTag[];
}

export function isPurgeRequest(request: Request): boolean {
  return request.method === "POST" && new URL(request.url).pathname === PURGE_PATH;
}

function reply(status: number, message?: string): Response {
  return new Response(message ?? null, {
    status,
    headers: { "cache-control": "private, no-store" },
  });
}

function parseBody(text: string): PurgeBody | undefined {
  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch {
    return undefined;
  }
  const body = parsed as Partial<PurgeBody> | null;
  if (typeof body !== "object" || body === null) return undefined;
  if (typeof body.release !== "string" || body.release === "") return undefined;
  if (body.mode !== "purge" && body.mode !== "invalidate") return undefined;
  if (!Array.isArray(body.tags)) return undefined;
  for (const entry of body.tags) {
    const { tag, raisedAt } = (entry ?? {}) as Partial<RaisedTag>;
    if (typeof tag !== "string" || tag === "") return undefined;
    if (typeof raisedAt !== "number" || !Number.isFinite(raisedAt)) return undefined;
  }
  return body as PurgeBody;
}

function hexBytes(hex: string): Uint8Array | undefined {
  if (hex.length === 0 || hex.length % 2 !== 0 || !/^[0-9a-fA-F]+$/.test(hex)) return undefined;
  return Uint8Array.from(hex.match(/../g)!, (pair) => Number.parseInt(pair, 16));
}

async function isSignedBy(secret: string, text: string, signature: string): Promise<boolean> {
  const expected = hexBytes(signature);
  if (!expected) return false;
  const key = await crypto.subtle.importKey(
    "raw",
    new TextEncoder().encode(secret),
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["verify"],
  );
  return crypto.subtle.verify("HMAC", key, expected, new TextEncoder().encode(text));
}

export function isControlHost(host: string): boolean {
  return host.split(":", 1)[0].toLowerCase().endsWith(".workers.dev");
}

export function controlHostResponse(): Response {
  return reply(404, "Not Found");
}

export function answerPurgeOnWorker(
  request: Request,
  env: Pick<Env, "RELEASES" | "OCEL_SLUG">,
  ctx: { exports: unknown },
): Promise<Response> | Response {
  const slug = env.OCEL_SLUG;
  if (!slug) return reply(404, "Not Found");
  const queues = (ctx.exports as { PurgeQueue: { getByName(name: string): PurgeDeps["queue"] } })
    .PurgeQueue;
  return answerPurge(request, {
    readServed: (release) => env.RELEASES.readServedRecord({ slug, release }),
    queue: queues.getByName(PURGE_QUEUE_NAME),
  });
}

export async function answerPurge(request: Request, deps: PurgeDeps): Promise<Response> {
  const text = await request.text();
  if (text.length > MAX_BODY_BYTES) return reply(413, "too large");

  const body = parseBody(text);
  if (!body) return reply(400, "bad request");
  if (body.tags.length > MAX_TAGS_PER_REQUEST) return reply(413, "too many tags");

  let served: ServedLookup;
  try {
    served = await deps.readServed(body.release);
  } catch (error) {
    console.error(`ocel: cannot tell whether release ${body.release} is served`, error);
    return reply(503, "unavailable");
  }
  if (served.kind === "not-served") return reply(204);

  const { purgeSecret } = served.record;
  const signature = request.headers.get(PURGE_SIGNATURE_HEADER);
  if (!purgeSecret || !signature || !(await isSignedBy(purgeSecret, text, signature))) {
    return reply(401, "unauthorized");
  }

  const { unstorable } = storedCacheTags(
    body.release,
    body.tags.map((entry) => entry.tag),
  );
  if (unstorable.length > 0) {
    (deps.warn ?? console.warn)(
      `ocel: ${unstorable.length} tag(s) of release ${body.release} are longer or wider than a cache tag Cloudflare stores, so nothing purges them: ${unstorable.join(", ")}`,
    );
  }
  const raisedAt = new Map<string, number>();
  for (const { tag, raisedAt: at } of body.tags) {
    if (unstorable.includes(tag)) continue;
    const stamped = `${body.release}|${tag}`;
    raisedAt.set(stamped, Math.max(raisedAt.get(stamped) ?? at, at));
  }
  if (raisedAt.size === 0) return reply(202);

  try {
    await deps.queue.raise(
      body.mode,
      [...raisedAt].map(([tag, at]) => ({ tag, raisedAt: at })),
    );
  } catch (error) {
    console.error(`ocel: the purge queue refused the tags of release ${body.release}`, error);
    return reply(503, "unavailable");
  }
  return reply(202);
}
