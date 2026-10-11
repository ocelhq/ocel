import { type PurgeSender, postPurge } from "@platform/edge-contract/purge";

import type { PurgeTags } from "./cdn-purge.mjs";

export interface CloudflarePurgeOptions extends Partial<PurgeSender> {
  url: string;
  release: string;
  secret: string;
  now?: () => number;
}

export function newCloudflarePurge(options: CloudflarePurgeOptions): PurgeTags {
  const { url, release, secret } = options;
  const sender: PurgeSender = {
    fetch: options.fetch ?? globalThis.fetch,
    sleep: options.sleep,
    random: options.random,
  };
  const now = options.now ?? Date.now;
  return (tags) => {
    const raisedAt = now();
    return postPurge(
      sender,
      url,
      secret,
      release,
      tags.map((tag) => ({ tag, mode: "purge", raisedAt })),
    );
  };
}
