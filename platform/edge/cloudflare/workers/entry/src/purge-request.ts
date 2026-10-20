import type { ServedRecordResult } from "@platform/cf-releases-store/store";
import { storedCacheTags } from "@platform/edge-contract/cache-tags";
import {
  type PurgeMode,
  purgePath,
  purgeSignatureHeader,
  tagsPerPurgeRequest,
  verifyPurge,
} from "@platform/edge-contract/purge";
import { parseStorageCoordinate } from "@platform/edge-contract/storage-key";
import { readCapped } from "./capped-body";
import type { Env } from "./env";
import { type EntryExports, newPurgeQueuePort } from "./exports";
import type { RaisedTag } from "./purge-backlog";
import type { ReleaseScope, TagTimes } from "./purge-times";
import type { ServedRecord } from "./served-record";

const PURGE_PROJECT_PARAM = "project";

export const maxPurgeBodyBytes = 512 * 1024;

export interface PurgeGatewayPorts {
  slug: string;
  readServed(storageCoordinate: string): Promise<ServedRecordResult<Pick<ServedRecord, "edge">>>;
  queue: Pick<PurgeQueuePort, "raise">;
  now?(): number;
  warn?(message: string): void;
}

interface PurgeBody {
  storageCoordinate: string;
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
  if (
    typeof body.storageCoordinate !== "string" ||
    parseStorageCoordinate(body.storageCoordinate) === undefined
  ) {
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

export function isControlHost(host: string): boolean {
  return host.split(":", 1)[0].toLowerCase().endsWith(".workers.dev");
}

export function respondOnControlHost(): Response {
  return reply(404, "Not Found");
}

export interface PurgeQueuePort {
  raise(scope: ReleaseScope, mode: PurgeMode, tags: RaisedTag[]): Promise<void>;
  readTimes(tags: string[]): Promise<TagTimes>;
}

function findPurgedProject(
  request: Request,
  env: Pick<Env, "OCEL_SLUG" | "OCEL_PREVIEW_GLOBAL">,
): string | undefined {
  if (env.OCEL_PREVIEW_GLOBAL !== "1") return env.OCEL_SLUG;
  return new URL(request.url).searchParams.get(PURGE_PROJECT_PARAM) ?? undefined;
}

export function answerPurgeOnWorker(
  request: Request,
  env: Pick<Env, "RELEASES" | "OCEL_SLUG" | "OCEL_PREVIEW_GLOBAL">,
  ctx: { exports: Pick<EntryExports, "PurgeQueue"> },
): Promise<Response> | Response {
  const slug = findPurgedProject(request, env);
  if (!slug) return reply(404, "Not Found");
  return answerPurge(request, {
    slug,
    readServed: (storageCoordinate) => env.RELEASES.readServedRecord({ slug, storageCoordinate }),
    queue: newPurgeQueuePort(ctx.exports),
  });
}

export async function answerPurge(request: Request, ports: PurgeGatewayPorts): Promise<Response> {
  const bytes = await readCapped(request, maxPurgeBodyBytes);
  if (!bytes) return reply(413, "too large");

  const body = parseBody(new TextDecoder().decode(bytes));
  if (!body) return reply(400, "bad request");
  if (body.tags.length > tagsPerPurgeRequest) return reply(413, "too many tags");

  let served: ServedRecordResult<Pick<ServedRecord, "edge">>;
  try {
    served = await ports.readServed(body.storageCoordinate);
  } catch (error) {
    console.error(
      `ocel: cannot tell whether the release at ${body.storageCoordinate} is served`,
      error,
    );
    return reply(503, "unavailable");
  }
  if (served.kind !== "record") return reply(204);

  const { purgeSecret } = served.record.edge;
  const signature = request.headers.get(purgeSignatureHeader);
  if (!purgeSecret || !signature || !(await verifyPurge(purgeSecret, bytes, signature))) {
    return reply(401, "unauthorized");
  }

  const { release } = parseStorageCoordinate(body.storageCoordinate)!;
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

  const raisedTags = [...raisedAt].map(([tag, at]) => ({ tag, raisedAt: at }));
  try {
    await ports.queue.raise(
      { slug: ports.slug, storageCoordinate: body.storageCoordinate },
      body.mode,
      raisedTags,
    );
  } catch (error) {
    console.error(`ocel: the purge queue refused the tags of release ${release}`, error);
    return reply(503, "unavailable");
  }
  return reply(202);
}
