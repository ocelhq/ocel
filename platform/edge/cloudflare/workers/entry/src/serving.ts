import type { PurgeMode } from "@platform/edge-contract/purge";
import { settledWithin } from "./cache";
import type { RaisedTag } from "./purge-backlog";
import { purgeInBand } from "./purge-inband";
import type { ServeProps } from "./serve-key";
import type { ServeFactory } from "./serve-objects";
import type { Serving } from "./serve-prerender";

const AWAITED_PURGE_MS = 3_000;

export interface ServingBase {
  serve: ServeFactory;
  queue: { raise(mode: PurgeMode, tags: RaisedTag[]): Promise<void> };
  waitUntil(promise: Promise<unknown>): void;
  now?(): number;
  awaitedPurgeMs?: number;
}

export function newServing(
  base: ServingBase,
  scope: Pick<ServeProps, "host" | "app" | "release">,
): Serving {
  return {
    serve: base.serve,
    release: scope.release,
    purge(groups, awaited) {
      const stub = base.serve({ props: { kind: "origin", ...scope } });
      const run = purgeInBand(groups, {
        purge: (mode, tags) => stub.purge(mode, tags),
        raise: (mode, tags) => base.queue.raise(mode, tags),
        now: base.now ?? Date.now,
      });
      base.waitUntil(run);
      if (!awaited) return Promise.resolve();
      return settledWithin(run, base.awaitedPurgeMs ?? AWAITED_PURGE_MS).then(() => {});
    },
  };
}
