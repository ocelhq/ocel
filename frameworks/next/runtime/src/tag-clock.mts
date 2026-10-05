import { areTagsExpired, mergeRecord, type TagRecord } from "@framework/next-cache";
import { holdEnd } from "@framework/node-runtime/background";
import { getNextHost } from "./host.mjs";
import { noteRevalidation } from "./revalidation-signal.mjs";
import { mirrorTag } from "./tags-manifest.mjs";
import { now } from "./use-cache-entry.mjs";
import type { UseCacheStore } from "./use-cache-store.mjs";

export interface TagClock {
  updateTags(tags: string[], durations?: { expire?: number }): Promise<void>;
  refreshTags(): Promise<void>;
  getExpiration(tags: string[]): Promise<number>;
  areTagsExpired(tags: string[], timestamp: number): boolean;
  areTagsStale(tags: string[], timestamp: number): boolean;
  readonly hasSynced: boolean;
}

interface ClockState {
  fingerprint: string;
  records: Map<string, TagRecord>;
  store: Promise<UseCacheStore | null> | undefined;
  etag: string | null;
  hasSynced: boolean;
  lastAttemptAt: number;
  inflight: Promise<void> | null;
}

const syncIntervalMs = 2_000;

const stateKey = Symbol.for("ocel.use-cache.tag-clock.v1");

function initialState(fingerprint: string): ClockState {
  return {
    fingerprint,
    records: new Map(),
    store: undefined,
    etag: null,
    hasSynced: false,
    lastAttemptAt: -Infinity,
    inflight: null,
  };
}

function sharedState(): ClockState {
  const fingerprint = process.env.OCEL_ISR_PREFIX ?? "";

  const host = globalThis as Record<symbol, ClockState | undefined>;
  const existing = host[stateKey];
  if (existing?.fingerprint === fingerprint) return existing;

  return (host[stateKey] = initialState(fingerprint));
}

const state = sharedState();

export function useCacheStore(): Promise<UseCacheStore | null> {
  if (state.store !== undefined) return state.store;
  const newStore = getNextHost().newUseCacheStore;
  if (!newStore) return Promise.resolve(null);
  const opening = newStore().catch(() => {
    if (state.store === opening) state.store = undefined;
    return null;
  });
  state.store = opening;
  return opening;
}

export function setTagClockStore(next: UseCacheStore | null | undefined): void {
  Object.assign(state, initialState(state.fingerprint), {
    store: next === undefined ? undefined : Promise.resolve(next),
  });
}

function observe(tag: string, incoming: TagRecord): void {
  const merged = mergeRecord(state.records.get(tag), incoming);
  state.records.set(tag, merged);
  mirrorTag(tag, merged);
}

async function sync(): Promise<void> {
  const backend = await useCacheStore();
  if (!backend) return;

  try {
    const read = await backend.readTagSnapshot(state.etag);

    if (read.status === "unusable") return;

    if (read.status === "fresh") {
      for (const [tag, record] of Object.entries(read.records)) observe(tag, record);
      state.etag = read.etag;
    }
    state.hasSynced = true;
  } catch {}
}

function startSync(): Promise<void> {
  state.lastAttemptAt = now();
  return (state.inflight = sync().finally(() => {
    state.inflight = null;
  }));
}

async function writeTags(tags: string[], at: number): Promise<void> {
  const backend = await useCacheStore();
  if (!backend) return;

  await Promise.all(
    tags.map(async (tag) => {
      const record = state.records.get(tag)!;
      try {
        await backend.writeTag(tag, { ...record, writtenAt: at });
      } catch {}
    }),
  );
}

export function recordTags(tags: string[], record: TagRecord): void {
  for (const tag of tags) observe(tag, record);
}

export const tagClock: TagClock = {
  async updateTags(tags, durations) {
    const at = now();
    for (const tag of tags) {
      const existing = state.records.get(tag) ?? {};
      const updated: TagRecord = durations
        ? {
            ...existing,
            stale: at,
            ...(durations.expire !== undefined ? { expired: at + durations.expire * 1000 } : {}),
          }
        : { ...existing, expired: at };
      state.records.set(tag, updated);
      mirrorTag(tag, updated);
    }
    if (tags.length > 0) noteRevalidation();

    await holdEnd(writeTags(tags, at));
  },

  async refreshTags() {
    if (state.inflight) return state.inflight;
    if (now() - state.lastAttemptAt < syncIntervalMs) return;

    return startSync();
  },

  async getExpiration(tags) {
    let expiration = 0;
    for (const tag of tags) {
      expiration = Math.max(expiration, state.records.get(tag)?.expired ?? 0);
    }
    return expiration;
  },

  areTagsExpired(tags, timestamp) {
    return tags.some((tag) => (state.records.get(tag)?.expired ?? 0) > timestamp);
  },

  areTagsStale(tags, timestamp) {
    return tags.some((tag) => (state.records.get(tag)?.stale ?? 0) > timestamp);
  },

  get hasSynced() {
    return state.hasSynced;
  },
};

export async function tagsExpireEntry(tags: string[], lastModified: number): Promise<boolean> {
  await tagClock.refreshTags();
  return areTagsExpired(tags, state.records, lastModified, Date.now());
}

export const clockMethods = {
  async refreshTags(): Promise<void> {
    await tagClock.refreshTags();
  },

  async getExpiration(tags: string[]): Promise<number> {
    return tagClock.getExpiration(tags);
  },

  async updateTags(tags: string[], durations?: { expire?: number }): Promise<void> {
    await tagClock.updateTags(tags, durations);
  },
};
