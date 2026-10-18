declare global {
  namespace Cloudflare {
    interface GlobalProps {
      durableNamespaces: "PurgeQueue";
    }
  }
}

export const PURGE_QUEUE_NAME = "purge";

export type EntryExports = Pick<Cloudflare.Exports, "Serve" | "PurgeQueue">;

export function openPurgeQueue(exports: Pick<EntryExports, "PurgeQueue">) {
  return exports.PurgeQueue.getByName(PURGE_QUEUE_NAME);
}
