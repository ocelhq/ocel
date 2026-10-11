import { storedCacheTags, type TagRecord } from "@framework/next-cache";
import {
  formatPurge,
  type PurgeEntry,
  type PurgeGroup,
  type PurgeMode,
  readPurgeEntry,
} from "@platform/edge-contract/purge";

const rememberedPurges = 2_000;

export const purgeHeaderBytes = 4_096;

interface Signal {
  total: number;
  recent: PurgeEntry[];
}

const stateKey = Symbol.for("ocel.next.purge-signal.v1");

function signal(): Signal {
  const host = globalThis as Record<symbol, Signal | undefined>;
  return (host[stateKey] ??= { total: 0, recent: [] });
}

export function notePurge(tag: string, record: TagRecord, now: number): void {
  const entry = readPurgeEntry(tag, record, now);
  if (!entry) return;
  const state = signal();
  state.recent.push(entry);
  state.total++;
  if (state.recent.length > rememberedPurges) {
    state.recent.splice(0, state.recent.length - rememberedPurges);
  }
}

export function purgeMark(): number {
  return signal().total;
}

export function purgedSince(mark: number): PurgeEntry[] {
  const { total, recent } = signal();
  return recent.slice(Math.max(0, recent.length - (total - mark)));
}

export function purgeHeaderFor(
  release: string,
  entries: readonly PurgeEntry[],
): string | undefined {
  const groups: PurgeGroup[] = [];
  for (const mode of ["purge", "invalidate"] as PurgeMode[]) {
    const { tags } = storedCacheTags(
      release,
      entries.filter((entry) => entry.mode === mode).map(({ tag }) => tag),
    );
    const kept: PurgeGroup = { mode, tags: [] };
    groups.push(kept);
    for (const tag of tags) {
      kept.tags.push(tag);
      if (formatPurge(groups)!.length > purgeHeaderBytes) {
        kept.tags.pop();
        return formatPurge(groups);
      }
    }
  }
  return formatPurge(groups);
}
