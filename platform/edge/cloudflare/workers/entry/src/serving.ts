import type { PurgeMode } from "@platform/edge-contract/purge";
import { settledWithin } from "./cache";
import type { RaisedTag } from "./purge-backlog";
import { purgeInBand } from "./purge-inband";
import { ORIGIN_PURGE_SCOPE, type OriginCoordinates } from "./serve-key";
import type { ServeFactory } from "./serve-objects";
import type { Serving } from "./serve-prerender";

export const AWAITED_PURGE_MS = 3_000;

export interface ServingPorts {
  serve: ServeFactory;
  queue: { raise(mode: PurgeMode, tags: RaisedTag[]): Promise<void> };
  waitUntil(promise: Promise<unknown>): void;
  now?(): number;
  awaitedPurgeMs?: number;
}

export function newServing(
  ports: ServingPorts,
  scope: Omit<OriginCoordinates, "variant"> & { slug: string; isrPrefix: string },
): Serving {
  return {
    serve: ports.serve,
    host: scope.host,
    release: scope.release,
    slug: scope.slug,
    isrPrefix: scope.isrPrefix,
    purge(groups, awaited) {
      const run = purgeInBand(groups, {
        cache: ports.serve({ props: ORIGIN_PURGE_SCOPE }),
        queue: ports.queue,
        now: ports.now ?? Date.now,
      });
      ports.waitUntil(run);
      if (!awaited) return Promise.resolve();
      return settledWithin(run, ports.awaitedPurgeMs ?? AWAITED_PURGE_MS).then(() => {});
    },
  };
}
