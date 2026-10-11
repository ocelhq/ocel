import { type PurgeSender, postPurge, purgeEntryOf } from "@platform/edge-contract/purge";

import type { PurgeTags } from "./cdn-purge.mjs";

export interface CloudflarePurgeOptions extends Partial<PurgeSender> {
  url: string;
  isrPrefix: string;
  secret: string;
}

export function newCloudflarePurge(options: CloudflarePurgeOptions): PurgeTags {
  const { url, isrPrefix, secret } = options;
  const sender: PurgeSender = {
    fetch: options.fetch ?? globalThis.fetch,
    sleep: options.sleep,
    random: options.random,
  };
  return (tags) =>
    postPurge(
      sender,
      url,
      secret,
      isrPrefix,
      [...tags].flatMap(([tag, record]) => purgeEntryOf(tag, record) ?? []),
    );
}
