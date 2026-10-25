import { type PurgeSender, postPurge, readPurgeEntry } from "@platform/edge-contract/purge";
import type { StorageCoordinate } from "@platform/edge-contract/storage-key";
import type { PurgeTags } from "./tag-records.mjs";

export interface EdgePurgeOptions extends Partial<PurgeSender> {
  url: string;
  coordinate: StorageCoordinate;
  secret: string;
  now?: () => number;
}

export function newEdgePurge(options: EdgePurgeOptions): PurgeTags {
  const { url, coordinate, secret } = options;
  const sender: PurgeSender = {
    fetch: options.fetch ?? globalThis.fetch,
    sleep: options.sleep,
    random: options.random,
  };
  const now = options.now ?? Date.now;
  return (tags) => {
    const at = now();
    return postPurge(
      sender,
      url,
      secret,
      coordinate,
      [...tags].flatMap(([tag, record]) => readPurgeEntry(tag, record, at) ?? []),
    );
  };
}
