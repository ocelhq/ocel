import type { PurgeGroup, PurgeMode } from "@platform/edge-contract/purge";
import { type PurgeOutcome, type RaisedTag, TAGS_PER_CALL } from "./purge-backlog";

export interface InBandPurgePorts {
  cache: { purge(mode: PurgeMode, tags: string[]): Promise<PurgeOutcome> };
  queue: { raise(mode: PurgeMode, tags: RaisedTag[]): Promise<void> };
  times: { record(mode: PurgeMode, tags: RaisedTag[]): Promise<void> };
  now(): number;
}

async function purgeChunk(
  { mode, tags, expiresAt }: PurgeGroup,
  ports: InBandPurgePorts,
): Promise<void> {
  const raisedAt = ports.now();
  const raised: RaisedTag[] = tags.map((tag) => ({
    tag,
    raisedAt,
    ...(expiresAt !== undefined && { expiresAt }),
  }));
  let recorded = false;
  try {
    await ports.times.record(mode, raised);
    recorded = true;
  } catch (error) {
    console.error(`ocel: the purge queue kept no time for ${tags.length} tag(s)`, error);
  }
  if (recorded) {
    try {
      if ((await ports.cache.purge(mode, tags)).ok) return;
    } catch {}
  }
  try {
    await ports.queue.raise(mode, raised);
  } catch (error) {
    console.error(`ocel: neither the cache nor the purge queue took ${tags.length} tag(s)`, error);
  }
}

export async function purgeInBand(
  groups: readonly PurgeGroup[],
  ports: InBandPurgePorts,
): Promise<void> {
  for (const group of groups) {
    for (let i = 0; i < group.tags.length; i += TAGS_PER_CALL) {
      await purgeChunk({ ...group, tags: group.tags.slice(i, i + TAGS_PER_CALL) }, ports);
    }
  }
}
