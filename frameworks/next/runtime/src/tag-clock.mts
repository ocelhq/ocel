import { AsyncLocalStorage } from "node:async_hooks";
import { areTagsExpired, mergeRecord } from "@framework/next-cache";
import { keptUntilSettled } from "@framework/node-runtime/background";
import { runTagWrites } from "@framework/node-runtime/tag-records";
import type { TagRecord } from "@platform/edge-contract/purge";
import { storageCoordinateVar } from "@platform/edge-contract/storage-key";
import { getNextHost } from "./host.mjs";
import { notePurge } from "./purge-signal.mjs";
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
  cursor: string | null;
  hasSynced: boolean;
  lastAttemptAt: number;
  inflight: Promise<void> | null;
}

const syncIntervalMs = 2_000;

const syncDeadlineMs = 3_000;

const outsideAnyRender = AsyncLocalStorage.snapshot();

const stateKey = Symbol.for("ocel.use-cache.tag-clock.v1");

function initialState(fingerprint: string): ClockState {
  return {
    fingerprint,
    records: new Map(),
    store: undefined,
    cursor: null,
    hasSynced: false,
    lastAttemptAt: -Infinity,
    inflight: null,
  };
}

function sharedState(): ClockState {
  const fingerprint = process.env[storageCoordinateVar] ?? "";

  const host = globalThis as Record<symbol, ClockState | undefined>;
  const existing = host[stateKey];
  if (existing?.fingerprint === fingerprint) return existing;

  return (host[stateKey] = initialState(fingerprint));
}

const state = sharedState();

function openUseCacheStore(): Promise<UseCacheStore | null> {
  if (state.store !== undefined) return state.store;
  const newStore = getNextHost().newUseCacheStore;
  if (!newStore) return Promise.resolve(null);
  const opening = newStore().catch((err) => {
    if (state.store === opening) state.store = undefined;
    throw err;
  });
  state.store = opening;
  return opening;
}

export function useCacheStore(): Promise<UseCacheStore | null> {
  return openUseCacheStore().catch(() => null);
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
    const read = await backend.readTagSnapshot(state.cursor);

    if (read.status === "unusable") return;

    if (read.status === "fresh") {
      for (const [tag, record] of Object.entries(read.records)) observe(tag, record);
      state.cursor = read.cursor;
    }
    state.hasSynced = true;
  } catch {}
}

function syncWithinDeadline(): Promise<void> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  const deadline = new Promise<void>((resolve) => {
    timer = setTimeout(resolve, syncDeadlineMs);
  });
  return Promise.race([outsideAnyRender(sync), deadline]).finally(() => clearTimeout(timer));
}

function startSync(): Promise<void> {
  state.lastAttemptAt = now();
  return (state.inflight = syncWithinDeadline().finally(() => {
    state.inflight = null;
  }));
}

function readInstant(): number {
  return Math.max(Date.now(), now());
}

const revalidations = new WeakMap<readonly string[], Promise<void>>();

async function writeRevalidation(
  tags: readonly string[],
  record: TagRecord,
  at: number,
): Promise<void> {
  const backend = await openUseCacheStore();
  if (!backend) return;

  const failures = await runTagWrites(tags, (tag) =>
    backend.writeTag(tag, { ...record, writtenAt: at }),
  );
  if (failures.length > 0) throw failures[0];
}

export function revalidateTags(
  tags: string | readonly string[],
  durations?: { expire?: number },
): Promise<void> {
  const list = typeof tags === "string" ? [tags] : tags;
  if (list.length === 0) return Promise.resolve();

  const known = typeof tags === "string" ? undefined : revalidations.get(tags);
  if (known) return known;

  const at = readInstant();
  const record: TagRecord = durations
    ? {
        stale: at,
        ...(durations.expire !== undefined ? { expired: at + durations.expire * 1000 } : {}),
      }
    : { expired: at };
  for (const tag of list) {
    observe(tag, record);
    notePurge(tag, record, at);
  }

  const written = keptUntilSettled(writeRevalidation(list, record, at));
  if (typeof tags !== "string") {
    revalidations.set(tags, written);
    const forget = () => {
      if (revalidations.get(tags) === written) revalidations.delete(tags);
    };
    written.then(forget, forget);
  }
  return written;
}

export const tagClock: TagClock = {
  updateTags: revalidateTags,

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
  return areTagsExpired(tags, state.records, lastModified, readInstant());
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
