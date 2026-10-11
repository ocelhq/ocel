import { type PurgeSender, postPurge, purgeEntryOf } from "@platform/edge-contract/purge";

import type { PurgeTags } from "./cdn-purge.mjs";

export interface CloudflarePurgeOptions extends Partial<PurgeSender> {
  url: string;
  release: string;
  secret: string;
}

export function newCloudflarePurge(options: CloudflarePurgeOptions): PurgeTags {
  const { url, release, secret } = options;
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
      release,
      [...tags].flatMap(([tag, record]) => purgeEntryOf(tag, record) ?? []),
    );
}
