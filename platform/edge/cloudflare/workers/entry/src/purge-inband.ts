import type { PurgeGroup, PurgeMode } from "@platform/edge-contract/purge";
import { type PurgeOutcome, type RaisedTag, TAGS_PER_CALL } from "./purge-backlog";

export interface InBandPurgePorts {
  purge(mode: PurgeMode, tags: string[]): Promise<PurgeOutcome>;
  raise(mode: PurgeMode, tags: RaisedTag[]): Promise<void>;
  now(): number;
}

async function purgeChunk(mode: PurgeMode, tags: string[], ports: InBandPurgePorts): Promise<void> {
  let landed = false;
  try {
    landed = (await ports.purge(mode, tags)).ok;
  } catch {}
  if (landed) return;
  const raisedAt = ports.now();
  try {
    await ports.raise(
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
