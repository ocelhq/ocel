// The cache the edge runs on. Next's edge outputs — middleware and every
// `runtime: 'edge'` route — execute in a dynamic worker that holds no
// credentials and no store bindings by design, so a cache handler bundled into
// it can only be an RPC client. This is the other end of that call: the main
// worker, which owns every binding, doing the storage on its behalf.
//
// The storage split is the same one the node tier keeps, and for the same
// reason (see cache-store.mts): fetch entries hold upstream response bodies, so
// they stay in the account's own S3 bucket and never reach an adopted edge
// store; tag records stay authoritative in DynamoDB; the edge reads tag state
// from the R2 replica the publisher republishes.
import {
  isGuardRejection,
  tagNamespace,
  tagRecordUpdate,
  type EdgeCacheRpc,
  type FetchCacheEntry,
  type TagRecord,
} from "@ocel/next-cache";
import { WorkerEntrypoint } from "cloudflare:workers";

import { awsServiceFetch, type AwsServiceFetch } from "./signing";
import {
  createTagClock,
  dropSnapshotMemo,
  parseJson,
  type ObjectStoreReader,
} from "./tag-clock";
import type { Env } from "./index";

// Raises a set of tag invalidations into the build's replica, resolving only
// once that replica holds them and throwing when it does not. The edge is a
// raiser and no longer a publisher: one publisher per build owns the merge and
// the write (workers/isr-writer), so a burst of invalidations coalesces there
// instead of contending on one R2 key.
export type SnapshotRaiser = (
  scope: string,
  records: Record<string, TagRecord>,
) => Promise<void>;

export interface EdgeCacheDeps {
  // The account-global AWS coordinates the deploy binds: one bucket and one
  // table for every project and app, scoped to a deployment by the ISR prefix
  // each call names. fetchBucket holds the fetch entries; table holds the
  // authoritative tag records.
  region: string;
  fetchBucket: string;
  table: string;
  aws: AwsServiceFetch;
  // The edge-local R2 binding the tag-snapshot replica is read from — a
  // different store from fetchBucket, deliberately: a replica is edge-readable
  // tag state, while a fetch entry holds an origin-private response body. Read
  // only: the replica's one writer is the build's publisher.
  snapshots: ObjectStoreReader;
  raise: SnapshotRaiser;
  waitUntil(promise: Promise<unknown>): void;
  now(): number;
}

// Next's IncrementalCache caps a fetch entry at 2 MB, but skips that check
// entirely when a custom handler is bound. So the cap is restored here rather
// than invented: it is the same bound the node tier's writes are already held
// to, and an entry the node path would have refused must not become a
// multi-megabyte RPC payload just by arriving from the edge. Over it the entry is
// simply not cached, which is how a cache refuses; raising would turn an
// oversized response into a broken render.
const maxEntryBytes = 2 * 1024 * 1024;

const fetchObjectKey = (scope: string, key: string) =>
  `${scope}/fetch-cache/${key}.cache.json`;

// An object key's separators are path structure and have to survive into the
// URL, so each segment is encoded on its own rather than the key as a whole.
const objectUrl = (deps: EdgeCacheDeps, key: string) =>
  `https://${deps.fetchBucket}.s3.${deps.region}.amazonaws.com/` +
  key.split("/").map(encodeURIComponent).join("/");

export function createEdgeCache(deps: EdgeCacheDeps): EdgeCacheRpc {
  const now = deps.now;

  return {
    // Nothing here throws: Next does not wrap a cache read in a try/catch, so a
    // failure has to degrade into a miss — which costs one upstream fetch — and
    // never into a render error.
    async fetchGet(scope, key, tags) {
      try {
        const response = await deps.aws("s3", objectUrl(deps, fetchObjectKey(scope, key)));
        if (!response.ok) return null;
        const entry = parseJson<FetchCacheEntry>(await response.text());
        if (!entry) return null;

        const all = entryTags(entry, tags);
        if (all.length === 0) return entry;

        // No PoP cache in front of this read, unlike the serving tiers': this is
        // the same isolate that republishes the snapshot, and a PoP entry is not
        // something a writer can drop the way it drops its memo. The serving
        // tiers can afford the cache's TTL because they only read.
        const clock = createTagClock({ isrPrefix: scope }, { store: deps.snapshots });
        // Unknown never serves. The colo tier decides the other way, but it is
        // already holding the bytes and its own refresh is what repairs the
        // snapshot; here, returning the entry would be a positive choice to hand
        // back data that may already be invalidated, against a contract that says
        // tag-invalidated content must never be served, not even once. A miss
        // costs an upstream fetch and repopulates, which is always safe.
        return (await clock.expired(all, entry.lastModified, now())) === false ? entry : null;
      } catch {
        return null;
      }
    },

    // The write must not hold the response open. A fetch entry is written on the
    // way out of a render — including a background revalidation the caller is
    // explicitly not waiting on — and a Server Action awaits the revalidation
    // drain before responding, so awaiting a cross-internet PUT here would put
    // S3's latency on the response. The node tier defers its write for the same
    // reason (cache-handler.mts).
    //
    // Deferring it onto this entrypoint's own context rather than returning it is
    // what keeps it off that path, and workerd does hold it: an RPC callee's
    // waitUntil task runs to completion after the method returned and after the
    // calling request responded (verified against workerd 2026-03-10). Returning
    // the promise would put the PUT back in front of whichever caller awaits it.
    //
    // Nothing here throws once the entry is a fetch entry, and for the same
    // reason a read does not: a cache that cannot store something declines to
    // store it. A write that never lands costs one cache entry — the next
    // request renders again.
    async fetchSet(scope, key, entry, tags) {
      // The one thing here that does throw, and the only door a page-level entry
      // could come through. Next's node entry templates are bundled into every
      // edge chunk as dead code (bd ocelhq-b7l), so a Next bump that started
      // routing a page write through the edge would arrive here with no change
      // on our side and silently compete with the worker's own ISR write-back
      // for the same key. The bundled client refuses one too, but a build's
      // bundle keeps running against a worker deployed long after it — so the
      // invariant lives on the side an old bundle cannot outlive.
      if (entry.value?.kind !== "FETCH") {
        throw new Error(
          `ocel: the edge cache stores fetch entries only, got kind ${entry.value?.kind}`,
        );
      }
      try {
        // Tags are stamped onto the stored value, which is where both tiers'
        // readers look for them (tagsOf), so an entry either tier writes is
        // legible to the other.
        const body = new TextEncoder().encode(
          JSON.stringify({ lastModified: entry.lastModified, value: { ...entry.value, tags } }),
        );
        if (body.byteLength > maxEntryBytes) return;

        deps.waitUntil(
          deps
            .aws("s3", objectUrl(deps, fetchObjectKey(scope, key)), {
              method: "PUT",
              headers: { "content-type": "application/json" },
              body,
            })
            .catch(() => undefined),
        );
      } catch {}
    },

    // Two writes, and both are required, but only one may fail the caller. The
    // DynamoDB record is authoritative and durable, so it is awaited and a real
    // failure surfaces: an invalidation that did not record did not happen. The
    // raise is replication — what the edge reads, never what decides — so it can
    // never surface. Next calls revalidateTag with no try/catch of its own and a
    // Server Action awaits the drain before responding, so a throw out of the
    // replication half would fail the very route raising the invalidation; a
    // replica the publisher could not replace just leaves the edge reading the
    // last one.
    //
    // A record written only to DynamoDB is invisible until some Lambda drains
    // the tag index and republishes — and serving from the stale snapshot is
    // exactly what stops any Lambda from running — so the raise is awaited
    // rather than deferred. It is the only thing that makes the invalidation
    // visible at all, and the writer answers only once the replica holds it,
    // which is what gives the isolate that raised it read-your-own-writes.
    async revalidateTags(scope, tags, durations) {
      if (tags.length === 0) return;

      const at = now();
      const record: TagRecord = durations
        ? {
            stale: at,
            ...(durations.expire !== undefined ? { expired: at + durations.expire * 1000 } : {}),
          }
        : { expired: at };

      const namespace = tagNamespace(scope);
      await Promise.all(
        tags.map((tag) => writeTagRecord(deps, namespace, tag, { ...record, writtenAt: at })),
      );

      // Authority first: a replica must never claim an invalidation the record
      // behind it does not have.
      try {
        await deps.raise(scope, Object.fromEntries(tags.map((tag) => [tag, record])));
      } catch (error) {
        console.error("ocel: could not raise the tag invalidation with the isr writer", error);
      } finally {
        // This isolate has just replaced the document its memo holds, so the memo
        // is known-stale — and without the drop an edge route that invalidates
        // and immediately re-reads would be answered from before its own write.
        dropSnapshotMemo({ isrPrefix: scope }, deps.snapshots);
      }
    },
  };
}

// The union tagsOf() computes for a fetch entry: what the caller was told this
// read depends on, plus what the writer recorded on the entry. Spelled out
// rather than routed through tagsOf, which dispatches on the stored `kind` —
// this door is fetch-only by construction, so the kind is the RPC's to know and
// not the payload's to declare.
function entryTags(entry: FetchCacheEntry, tags: string[]): string[] {
  const stored = entry.value?.tags;
  return Array.isArray(stored) ? [...tags, ...(stored as string[])] : tags;
}

// tagRecordUpdate returns the UpdateItem wire body itself, so the signed call is
// that object stringified under the matching X-Amz-Target — no translation, and
// no way for the two tiers to drift into writing different rows.
async function writeTagRecord(
  deps: EdgeCacheDeps,
  namespace: string,
  tag: string,
  record: { stale?: number; expired?: number; writtenAt: number },
): Promise<void> {
  const response = await deps.aws("dynamodb", `https://dynamodb.${deps.region}.amazonaws.com/`, {
    method: "POST",
    headers: {
      "content-type": "application/x-amz-json-1.0",
      "x-amz-target": "DynamoDB_20120810.UpdateItem",
    },
    body: JSON.stringify(tagRecordUpdate(deps.table, namespace, tag, record)),
  });
  if (response.ok) return;

  // A rejected guard is the common path, not a failure: the update's condition
  // is monotonic, both tiers raise every invalidation, and the second write for
  // an event always loses to a watermark that is already at least as strict.
  const error = await dynamoError(response);
  if (!isGuardRejection(error)) throw error;
}

// DynamoDB reports a failure as a 4xx whose body names the exception in `__type`,
// after a `#`. The SDK turns that suffix into the error's `name`, which is what
// isGuardRejection reads — so the raw path reconstructs the same error the SDK
// path would have thrown rather than teaching the shared predicate a second shape.
async function dynamoError(response: Response): Promise<Error> {
  const body = await response.text();
  const type = parseJson<{ __type?: string }>(body)?.__type;
  const error = new Error(`dynamodb ${response.status}: ${body}`);
  if (type) error.name = type.slice(type.indexOf("#") + 1);
  return error;
}

// The ISR writer worker as this file reaches it: one account-level worker, bound
// as a service so a raise never leaves the account's own network. Narrowed to
// the one call rather than typed as Fetcher so nothing here depends on the rest.
export interface IsrWriterBinding {
  fetch(request: Request): Promise<Response>;
}

// tagRaiser posts a build's invalidations to its publisher. The bearer secret is
// the deploy's own write secret — the same one its entry reads and writes carry
// — so the raise authenticates at the writer's one auth boundary rather than on
// the binding being reachable at all.
//
// A raise the writer did not accept throws, and the caller logs it: the records
// are still held by whoever raised them, and the merge is idempotent, so a
// throttled build repairs itself on the next invalidation.
export function tagRaiser(
  writer: IsrWriterBinding | undefined,
  secret: string | undefined,
): SnapshotRaiser {
  return async (scope, records) => {
    if (!writer || !secret) {
      throw new Error("ocel: no isr writer is bound, so this build has no publisher to raise to");
    }
    const response = await writer.fetch(
      new Request(`https://isr-writer/${scope}/tags`, {
        method: "POST",
        headers: { authorization: `Bearer ${secret}`, "content-type": "application/json" },
        body: JSON.stringify({ records }),
      }),
    );
    if (!response.ok) {
      throw new Error(`ocel: the isr writer refused the tag raise with ${response.status}`);
    }
  };
}

// CacheEntrypoint is how the dynamic worker reaches all of the above: it is
// exported from the worker's main module, so the fetch handler can hand the
// bundle `ctx.exports.CacheEntrypoint` as a loopback binding that outlives every
// request.
//
// Its dependencies are read per call rather than captured at construction: this
// worker script is frozen and outlives its deployments (ADR 0002), and a
// substrate that binds none of them must degrade to an uncached edge rather than
// fail to boot — which is why every one of these bindings is optional and a
// missing one answers like an empty cache.
//
// The one thing no binding can carry is the write secret a raise authenticates
// with: it belongs to the Deployment, not to the script, so it rides the props
// the stub is created with (see resolveRouteDeps).
export interface CacheEntrypointProps {
  isrWriteSecret?: string;
}

export class CacheEntrypoint
  extends WorkerEntrypoint<Env, CacheEntrypointProps>
  implements EdgeCacheRpc
{
  private cache(): EdgeCacheRpc | null {
    const { OCEL_AWS_REGION, OCEL_ISR_BUCKET, OCEL_STATE_TABLE, OCEL_CACHE_STORE } = this.env;
    const aws = awsServiceFetch(
      this.env.OCEL_EDGE_ACCESS_KEY_ID,
      this.env.OCEL_EDGE_SECRET_KEY,
      OCEL_AWS_REGION,
    );
    if (!aws || !OCEL_AWS_REGION || !OCEL_ISR_BUCKET || !OCEL_STATE_TABLE || !OCEL_CACHE_STORE) {
      return null;
    }
    return createEdgeCache({
      region: OCEL_AWS_REGION,
      fetchBucket: OCEL_ISR_BUCKET,
      table: OCEL_STATE_TABLE,
      aws,
      snapshots: OCEL_CACHE_STORE,
      raise: tagRaiser(this.env.ISR_WRITER, this.ctx.props?.isrWriteSecret),
      waitUntil: (promise) => this.ctx.waitUntil(promise),
      now: Date.now,
    });
  }

  async fetchGet(scope: string, key: string, tags: string[]): Promise<FetchCacheEntry | null> {
    return (await this.cache()?.fetchGet(scope, key, tags)) ?? null;
  }

  async fetchSet(
    scope: string,
    key: string,
    entry: FetchCacheEntry,
    tags: string[],
  ): Promise<void> {
    await this.cache()?.fetchSet(scope, key, entry, tags);
  }

  async revalidateTags(
    scope: string,
    tags: string[],
    durations?: { expire?: number },
  ): Promise<void> {
    await this.cache()?.revalidateTags(scope, tags, durations);
  }
}
