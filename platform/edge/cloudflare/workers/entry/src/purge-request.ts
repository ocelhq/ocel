import { storedCacheTags } from "@framework/next-cache";
import {
  type PurgeMode,
  purgePath,
  purgeSignatureHeader,
  readReleaseToken,
  tagsPerPurgeRequest,
} from "@platform/edge-contract/purge";
import type { Env } from "./env";
import type { RaisedTag } from "./purge-backlog";

const PURGE_QUEUE_NAME = "purge";

export const maxPurgeBodyBytes = 512 * 1024;

export type ServedLookup =
  | { kind: "record"; record: { purgeSecret?: string } }
  | { kind: "not-served" };

export interface PurgeGatewayPorts {
  readServed(isrPrefix: string): Promise<ServedLookup>;
  queue: { raise(mode: PurgeMode, tags: RaisedTag[]): Promise<void> };
  now?(): number;
  warn?(message: string): void;
}

interface PurgeBody {
  isrPrefix: string;
  mode: PurgeMode;
  tags: RaisedTag[];
}

export function isPurgeRequest(request: Request): boolean {
  return request.method === "POST" && new URL(request.url).pathname === purgePath;
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
  if (typeof body.isrPrefix !== "string" || readReleaseToken(body.isrPrefix) === undefined) {
    return undefined;
  }
  if (body.mode !== "purge" && body.mode !== "invalidate") return undefined;
  if (!Array.isArray(body.tags)) return undefined;
  for (const entry of body.tags) {
    const { tag, raisedAt } = (entry ?? {}) as Partial<RaisedTag>;
    if (typeof tag !== "string" || tag === "") return undefined;
    if (typeof raisedAt !== "number" || !Number.isFinite(raisedAt)) return undefined;
  }
  return body as PurgeBody;
}

function decodeHex(hex: string): Uint8Array | undefined {
  if (hex.length === 0 || hex.length % 2 !== 0 || !/^[0-9a-fA-F]+$/.test(hex)) return undefined;
  return Uint8Array.from(hex.match(/../g)!, (pair) => Number.parseInt(pair, 16));
}

async function readCapped(request: Request): Promise<Uint8Array | undefined> {
  const declared = Number(request.headers.get("content-length") ?? 0);
  if (declared > maxPurgeBodyBytes) return undefined;
  if (!request.body) return new Uint8Array();
  const reader = request.body.getReader();
  const chunks: Uint8Array[] = [];
  let size = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    size += value.byteLength;
    if (size > maxPurgeBodyBytes) {
      await reader.cancel();
      return undefined;
    }
    chunks.push(value);
  }
  const bytes = new Uint8Array(size);
  let offset = 0;
  for (const chunk of chunks) {
    bytes.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return bytes;
}

async function isSignedBy(secret: string, bytes: Uint8Array, signature: string): Promise<boolean> {
  const expected = decodeHex(signature);
  if (!expected) return false;
  const key = await crypto.subtle.importKey(
    "raw",
    new TextEncoder().encode(secret),
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["verify"],
  );
  return crypto.subtle.verify("HMAC", key, expected, bytes);
}

export function isControlHost(host: string): boolean {
  return host.split(":", 1)[0].toLowerCase().endsWith(".workers.dev");
}

export function respondOnControlHost(): Response {
  return reply(404, "Not Found");
}

export function openPurgeQueue(exports: unknown): PurgeGatewayPorts["queue"] {
  const queues = (
    exports as { PurgeQueue: { getByName(name: string): PurgeGatewayPorts["queue"] } }
  ).PurgeQueue;
  return queues.getByName(PURGE_QUEUE_NAME);
}

export function answerPurgeOnWorker(
  request: Request,
  env: Pick<Env, "RELEASES" | "OCEL_SLUG">,
  ctx: { exports: unknown },
): Promise<Response> | Response {
  const slug = env.OCEL_SLUG;
  if (!slug) return reply(404, "Not Found");
  return answerPurge(request, {
    readServed: (isrPrefix) => env.RELEASES.readServedRecord({ slug, isrPrefix }),
    queue: openPurgeQueue(ctx.exports),
  });
}

export async function answerPurge(request: Request, ports: PurgeGatewayPorts): Promise<Response> {
  const bytes = await readCapped(request);
  if (!bytes) return reply(413, "too large");

  const body = parseBody(new TextDecoder().decode(bytes));
  if (!body) return reply(400, "bad request");
  if (body.tags.length > tagsPerPurgeRequest) return reply(413, "too many tags");

  let served: ServedLookup;
  try {
    served = await ports.readServed(body.isrPrefix);
  } catch (error) {
    console.error(`ocel: cannot tell whether the release at ${body.isrPrefix} is served`, error);
    return reply(503, "unavailable");
  }
  if (served.kind === "not-served") return reply(204);

  const { purgeSecret } = served.record;
  const signature = request.headers.get(purgeSignatureHeader);
  if (!purgeSecret || !signature || !(await isSignedBy(purgeSecret, bytes, signature))) {
    return reply(401, "unauthorized");
  }

  const release = readReleaseToken(body.isrPrefix)!;
  const now = (ports.now ?? Date.now)();
  const unstorable: string[] = [];
  const raisedAt = new Map<string, number>();
  for (const { tag, raisedAt: at } of body.tags) {
    const [stamped] = storedCacheTags(release, [tag]).tags;
    if (stamped === undefined) {
      unstorable.push(tag);
      continue;
    }
    const clamped = Math.min(at, now);
    raisedAt.set(stamped, Math.max(raisedAt.get(stamped) ?? clamped, clamped));
  }
  if (unstorable.length > 0) {
    (ports.warn ?? console.warn)(
      `ocel: ${unstorable.length} tag(s) of release ${release} are longer or wider than a cache tag Cloudflare stores, so nothing purges them: ${unstorable.join(", ")}`,
    );
  }
  if (raisedAt.size === 0) return reply(202);

  try {
    await ports.queue.raise(
      body.mode,
      [...raisedAt].map(([tag, at]) => ({ tag, raisedAt: at })),
    );
  } catch (error) {
    console.error(`ocel: the purge queue refused the tags of release ${release}`, error);
    return reply(503, "unavailable");
  }
  return reply(202);
}
