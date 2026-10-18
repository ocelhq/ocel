import type { PurgeQueuePort } from "./purge-request";

declare global {
  namespace Cloudflare {
    interface GlobalProps {
      durableNamespaces: "PurgeQueue";
    }
  }
}

export type EntryExports = Pick<Cloudflare.Exports, "Serve" | "PurgeQueue">;

export function openPurgeQueue(exports: Pick<EntryExports, "PurgeQueue">, slug: string) {
  return exports.PurgeQueue.getByName(slug);
}

export function newPurgeQueuePort(
  exports: Pick<EntryExports, "PurgeQueue">,
): Pick<PurgeQueuePort, "raise"> {
  return {
    raise: (scope, mode, tags) => openPurgeQueue(exports, scope.slug).raise(scope, mode, tags),
  };
}
