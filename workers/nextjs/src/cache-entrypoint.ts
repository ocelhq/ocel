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

export type SnapshotRaiser = (
  scope: string,
  records: Record<string, TagRecord>,
) => Promise<void>;

export interface EdgeCacheDeps {
  region: string;
  fetchBucket: string;
  table: string;
  aws: AwsServiceFetch;
  snapshots: ObjectStoreReader;
  raise: SnapshotRaiser;
  waitUntil(promise: Promise<unknown>): void;
  now(): number;
}

const maxEntryBytes = 2 * 1024 * 1024;

const fetchObjectKey = (scope: string, key: string) =>
  `${scope}/fetch-cache/${key}.cache.json`;

const objectUrl = (deps: EdgeCacheDeps, key: string) =>
  `https://${deps.fetchBucket}.s3.${deps.region}.amazonaws.com/` +
  key.split("/").map(encodeURIComponent).join("/");

export function createEdgeCache(deps: EdgeCacheDeps): EdgeCacheRpc {
  const now = deps.now;

  return {
    async fetchGet(scope, key, tags) {
      try {
        const response = await deps.aws("s3", objectUrl(deps, fetchObjectKey(scope, key)));
        if (!response.ok) return null;
        const entry = parseJson<FetchCacheEntry>(await response.text());
        if (!entry) return null;

        const all = entryTags(entry, tags);
        if (all.length === 0) return entry;

        const clock = createTagClock({ isrPrefix: scope }, { store: deps.snapshots });
        return (await clock.expired(all, entry.lastModified, now())) === false ? entry : null;
      } catch {
        return null;
      }
    },

    async fetchSet(scope, key, entry, tags) {
      if (entry.value?.kind !== "FETCH") {
        throw new Error(
          `ocel: the edge cache stores fetch entries only, got kind ${entry.value?.kind}`,
        );
      }
      try {
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

      try {
        await deps.raise(scope, Object.fromEntries(tags.map((tag) => [tag, record])));
      } catch (error) {
        console.error("ocel: could not raise the tag invalidation with the isr writer", error);
      } finally {
        dropSnapshotMemo({ isrPrefix: scope }, deps.snapshots);
      }
    },
  };
}

function entryTags(entry: FetchCacheEntry, tags: string[]): string[] {
  const stored = entry.value?.tags;
  return Array.isArray(stored) ? [...tags, ...(stored as string[])] : tags;
}

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

  const error = await dynamoError(response);
  if (!isGuardRejection(error)) throw error;
}

async function dynamoError(response: Response): Promise<Error> {
  const body = await response.text();
  const type = parseJson<{ __type?: string }>(body)?.__type;
  const error = new Error(`dynamodb ${response.status}: ${body}`);
  if (type) error.name = type.slice(type.indexOf("#") + 1);
  return error;
}

export interface IsrWriterBinding {
  fetch(request: Request): Promise<Response>;
}

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
