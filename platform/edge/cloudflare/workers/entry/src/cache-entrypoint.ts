import { WorkerEntrypoint } from "cloudflare:workers";
import {
  type EdgeCacheRpc,
  type FetchCacheEntry,
  storedCacheTags,
  type TagRecord,
} from "@framework/next-cache";
import {
  type PurgeGroup,
  type PurgeMode,
  readPurgeEntry,
  readReleaseToken,
} from "@platform/edge-contract/purge";
import { settledWithin } from "./cache";
import type { CacheEntrypointProps, Env, IsrWriterBinding } from "./env";
import { purgeInBand } from "./purge-inband";
import { openInBandQueue, openPurgeQueue } from "./purge-request";
import { ORIGIN_PURGE_SCOPE } from "./serve-key";
import type { ServeFactory } from "./serve-objects";
import { AWAITED_PURGE_MS } from "./serving";
import { createTagClock, dropSnapshotMemo, type ObjectStoreReader, parseJson } from "./tag-clock";

export type SnapshotRaiser = (scope: string, records: Record<string, TagRecord>) => Promise<void>;

export interface FetchEntryStore {
  get(key: string): Promise<{ text(): Promise<string> } | null>;
  put(
    key: string,
    value: Uint8Array,
    options?: { httpMetadata?: { contentType?: string } },
  ): Promise<unknown>;
}

export interface EdgeCacheDeps {
  scope: string;
  entries: FetchEntryStore;
  snapshots: ObjectStoreReader;
  raise: SnapshotRaiser;
  purge(groups: PurgeGroup[]): Promise<void>;
  waitUntil(promise: Promise<unknown>): void;
  now(): number;
}

const maxEntryBytes = 2 * 1024 * 1024;

const fetchObjectKey = (scope: string, key: string) => `${scope}/fetch-cache/${key}.cache.json`;

export function createEdgeCache(deps: EdgeCacheDeps): EdgeCacheRpc {
  const now = deps.now;

  return {
    async fetchGet(scope, key, tags) {
      if (scope !== deps.scope) return null;
      try {
        const object = await deps.entries.get(fetchObjectKey(scope, key));
        if (!object) return null;
        const entry = parseJson<FetchCacheEntry>(await object.text());
        if (!entry) return null;

        const all = entryTags(entry, tags);
        if (all.length === 0) return entry;

        const clock = createTagClock({ isrPrefix: scope }, { store: deps.snapshots });
        return (await clock.freshness(all, entry.lastModified, now())) === "fresh" ? entry : null;
      } catch {
        return null;
      }
    },

    async fetchSet(scope, key, entry, tags) {
      if (scope !== deps.scope) return;
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
          deps.entries
            .put(fetchObjectKey(scope, key), body, {
              httpMetadata: { contentType: "application/json" },
            })
            .catch(() => undefined),
        );
      } catch {}
    },

    async revalidateTags(scope, tags, durations) {
      if (scope !== deps.scope) {
        throw new Error(`ocel: this edge bundle's cache is bound to ${deps.scope}, not ${scope}`);
      }
      if (tags.length === 0) return;

      const at = now();
      const record: TagRecord = durations
        ? {
            stale: at,
            ...(durations.expire !== undefined ? { expired: at + durations.expire * 1000 } : {}),
          }
        : { expired: at };

      try {
        await deps.raise(scope, Object.fromEntries(tags.map((tag) => [tag, record])));
      } finally {
        dropSnapshotMemo({ isrPrefix: scope }, deps.snapshots);
      }
      const groups = buildPurgeGroups(scope, tags, record, at);
      if (groups.length > 0) await deps.purge(groups);
    },
  };
}

function buildPurgeGroups(
  scope: string,
  tags: string[],
  record: TagRecord,
  now: number,
): PurgeGroup[] {
  const release = readReleaseToken(scope);
  if (release === undefined) return [];
  const byMode = new Map<PurgeMode, string[]>();
  for (const tag of storedCacheTags(release, tags).tags) {
    const entry = readPurgeEntry(tag, record, now);
    if (!entry) continue;
    byMode.set(entry.mode, [...(byMode.get(entry.mode) ?? []), tag]);
  }
  return [...byMode].map(([mode, grouped]) => ({ mode, tags: grouped }));
}

function entryTags(entry: FetchCacheEntry, tags: string[]): string[] {
  const stored = entry.value?.tags;
  return Array.isArray(stored) ? [...tags, ...(stored as string[])] : tags;
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

export class CacheEntrypoint
  extends WorkerEntrypoint<Env, CacheEntrypointProps>
  implements EdgeCacheRpc
{
  private cache(): EdgeCacheRpc | null {
    const { OCEL_CACHE_STORE } = this.env;
    const scope = this.ctx.props?.scope;
    if (!OCEL_CACHE_STORE || !scope) return null;

    return createEdgeCache({
      scope,
      entries: OCEL_CACHE_STORE,
      snapshots: OCEL_CACHE_STORE,
      raise: tagRaiser(this.env.ISR_WRITER, this.ctx.props?.purgeSecret),
      purge: (groups) => this.purgeServed(groups),
      waitUntil: (promise) => this.ctx.waitUntil(promise),
      now: Date.now,
    });
  }

  private async purgeServed(groups: PurgeGroup[]): Promise<void> {
    const serve = (this.ctx.exports as { Serve?: ServeFactory }).Serve;
    if (!serve) return;
    const run = purgeInBand(groups, {
      cache: serve({ props: ORIGIN_PURGE_SCOPE }),
      queue: openInBandQueue(this.ctx.exports),
      times: openPurgeQueue(this.ctx.exports),
      now: Date.now,
    });
    this.ctx.waitUntil(run);
    await settledWithin(run, AWAITED_PURGE_MS);
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
