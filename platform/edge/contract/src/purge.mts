import { createHmac } from "node:crypto";

import type { TagRecord } from "@framework/next-cache";

export const purgePath = "/.ocel/purge";

export const purgeSignatureHeader = "x-ocel-signature";

export const tagsPerPurgeRequest = 500;

const attempts = 4;
const backoffMs = 500;
const backoffCeilingMs = 10_000;

export type PurgeMode = "purge" | "invalidate";

export interface PurgeEntry {
  tag: string;
  mode: PurgeMode;
  raisedAt: number;
}

export interface PurgeSender {
  fetch: typeof fetch;
  sleep?: (ms: number) => Promise<void>;
  random?: () => number;
}

export function purgeEntryOf(tag: string, record: TagRecord): PurgeEntry | undefined {
  const raisedAt = record.stale ?? record.expired;
  if (raisedAt === undefined) return undefined;
  return {
    tag,
    mode: record.expired !== undefined && record.expired <= raisedAt ? "purge" : "invalidate",
    raisedAt,
  };
}

export function readReleaseToken(isrPrefix: string): string | undefined {
  const segments = isrPrefix.split("/");
  if (segments.length !== 5 || segments[4] !== "isr" || segments.includes("")) return undefined;
  return segments[3];
}

export function derivePurgeSecret(seed: string, isrPrefix: string): string {
  return createHmac("sha256", seed).update(isrPrefix).digest("hex");
}

function isRetryable(status: number): boolean {
  return status === 408 || status === 429 || status >= 500;
}

async function post(sender: PurgeSender, url: string, secret: string, body: string): Promise<void> {
  const pause = sender.sleep ?? ((ms: number) => new Promise((done) => setTimeout(done, ms)));
  const random = sender.random ?? Math.random;
  const signature = createHmac("sha256", secret).update(body).digest("hex");
  let last = "";
  for (let attempt = 0; attempt < attempts; attempt++) {
    if (attempt > 0) {
      await pause(random() * Math.min(backoffMs * 2 ** (attempt - 1), backoffCeilingMs));
    }
    try {
      const res = await sender.fetch(url, {
        method: "POST",
        headers: { "content-type": "application/json", [purgeSignatureHeader]: signature },
        body,
      });
      await res.body?.cancel();
      if (res.ok) return;
      last = String(res.status);
      if (!isRetryable(res.status)) break;
    } catch (error) {
      last = error instanceof Error ? error.message : String(error);
    }
  }
  throw new Error(`ocel: the purge endpoint ${url} did not take the purge: ${last}`);
}

export async function postPurge(
  sender: PurgeSender,
  url: string,
  secret: string,
  isrPrefix: string,
  entries: readonly PurgeEntry[],
): Promise<void> {
  for (const mode of ["purge", "invalidate"] as const) {
    const tags = entries
      .filter((entry) => entry.mode === mode)
      .map(({ tag, raisedAt }) => ({ tag, raisedAt }));
    for (let i = 0; i < tags.length; i += tagsPerPurgeRequest) {
      const body = JSON.stringify({
        isrPrefix,
        mode,
        tags: tags.slice(i, i + tagsPerPurgeRequest),
      });
      await post(sender, url, secret, body);
    }
  }
}
