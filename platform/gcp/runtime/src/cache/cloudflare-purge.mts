import { type PurgeSender, postPurge, readPurgeEntry } from "@platform/edge-contract/purge";

import type { PurgeTags } from "./cdn-purge.mjs";

export interface CloudflarePurgeOptions extends Partial<PurgeSender> {
  url: string;
  pagesPrefix: string;
  secret: string;
  now?: () => number;
}

export function newCloudflarePurge(options: CloudflarePurgeOptions): PurgeTags {
  const { url, pagesPrefix, secret } = options;
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
      pagesPrefix,
      [...tags].flatMap(([tag, record]) => readPurgeEntry(tag, record, at) ?? []),
    );
  };
}
