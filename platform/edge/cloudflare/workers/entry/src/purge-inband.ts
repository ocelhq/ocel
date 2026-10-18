import type { PurgeGroup } from "@platform/edge-contract/purge";
import type { PurgeQueuePort } from "./purge-request";
import type { ReleaseScope } from "./purge-times";

export interface InBandPurgePorts {
  queue: Pick<PurgeQueuePort, "raise">;
  scope: ReleaseScope;
  now(): number;
}

export async function purgeInBand(
  groups: readonly PurgeGroup[],
  ports: InBandPurgePorts,
): Promise<void> {
  const raisedAt = ports.now();
  for (const { mode, tags } of groups) {
    if (tags.length === 0) continue;
    try {
      await ports.queue.raise(
        ports.scope,
        mode,
        tags.map((tag) => ({ tag, raisedAt })),
      );
    } catch (error) {
      console.error(`ocel: the purge queue did not take ${tags.length} tag(s)`, error);
    }
  }
}
