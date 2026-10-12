import { type PurgeSender, postPurge, readPurgeEntry } from "@platform/edge-contract/purge";

import type { PurgeTags } from "./cdn-purge.mjs";

export interface CloudflarePurgeOptions extends Partial<PurgeSender> {
  url: string;
  isrPrefix: string;
  secret: string;
  now?: () => number;
}

export function newCloudflarePurge(options: CloudflarePurgeOptions): PurgeTags {
  const { url, isrPrefix, secret } = options;
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
      isrPrefix,
      [...tags].flatMap(([tag, record]) => readPurgeEntry(tag, record, at) ?? []),
    );
  };
}
