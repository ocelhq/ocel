import type { PurgeGroup, PurgeMode } from "@platform/edge-contract/purge";
import { type PurgeOutcome, type RaisedTag, TAGS_PER_CALL } from "./purge-backlog";

export interface InBandPurgePorts {
  cache: { purge(mode: PurgeMode, tags: string[]): Promise<PurgeOutcome> };
  queue: { raise(mode: PurgeMode, tags: RaisedTag[]): Promise<void> };
  times: { record(mode: PurgeMode, tags: RaisedTag[]): Promise<void> };
  now(): number;
}

async function purgeChunk(mode: PurgeMode, tags: string[], ports: InBandPurgePorts): Promise<void> {
  const raisedAt = ports.now();
  try {
    await ports.times.record(
      mode,
      tags.map((tag) => ({ tag, raisedAt })),
    );
  } catch (error) {
    console.error(`ocel: the purge queue kept no time for ${tags.length} tag(s)`, error);
  }
  let purged = false;
  try {
    purged = (await ports.cache.purge(mode, tags)).ok;
  } catch {}
  if (purged) return;
  try {
    await ports.queue.raise(
      mode,
      tags.map((tag) => ({ tag, raisedAt })),
    );
  } catch (error) {
    console.error(`ocel: neither the cache nor the purge queue took ${tags.length} tag(s)`, error);
  }
}

export async function purgeInBand(
  groups: readonly PurgeGroup[],
  ports: InBandPurgePorts,
): Promise<void> {
  for (const { mode, tags } of groups) {
    for (let i = 0; i < tags.length; i += TAGS_PER_CALL) {
      await purgeChunk(mode, tags.slice(i, i + TAGS_PER_CALL), ports);
    }
  }
}
