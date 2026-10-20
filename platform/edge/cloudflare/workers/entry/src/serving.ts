import { purgeInBand } from "./purge-inband";
import type { PurgeQueuePort } from "./purge-request";
import type { RenderCoordinates } from "./serve-key";
import type { ServeFactory } from "./serve-objects";
import type { Serving } from "./serve-prerender";
import { settledWithin } from "./settled";

export const AWAITED_PURGE_MS = 3_000;

export interface ServingPorts {
  serve: ServeFactory;
  queue: Pick<PurgeQueuePort, "raise">;
  waitUntil(promise: Promise<unknown>): void;
  now?(): number;
  awaitedPurgeMs?: number;
}

export function newServing(
  ports: ServingPorts,
  scope: Omit<RenderCoordinates, "variant"> & { slug: string; storageCoordinate: string },
): Serving {
  return {
    serve: ports.serve,
    host: scope.host,
    release: scope.release,
    slug: scope.slug,
    storageCoordinate: scope.storageCoordinate,
    purge(groups, awaited) {
      const run = purgeInBand(groups, {
        queue: ports.queue,
        scope: { slug: scope.slug, storageCoordinate: scope.storageCoordinate },
        now: ports.now ?? Date.now,
      });
      ports.waitUntil(run);
      if (!awaited) return Promise.resolve();
      return settledWithin(run, ports.awaitedPurgeMs ?? AWAITED_PURGE_MS).then(() => {});
    },
  };
}
