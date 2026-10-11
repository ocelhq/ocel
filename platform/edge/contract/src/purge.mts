import { createHmac } from "node:crypto";

import type { TagRecord } from "@framework/next-cache";

export const purgePath = "/.ocel/purge";

export const purgeSignatureHeader = "x-ocel-signature";

export const tagsPerPurgeRequest = 500;

const attempts = 4;
const backoffMs = 500;
const backoffCeilingMs = 10_000;
const attemptTimeoutMs = 5_000;

export type PurgeMode = "purge" | "invalidate";

export interface PurgeEntry {
  tag: string;
  mode: PurgeMode;
  raisedAt: number;
}

export class PurgeRefusedError extends Error {
  readonly status: number | undefined;

  constructor(message: string, status: number | undefined) {
    super(message);
    this.name = "PurgeRefusedError";
    this.status = status;
  }
}

export interface PurgeSender {
  fetch: typeof fetch;
  sleep?: (ms: number) => Promise<void>;
  random?: () => number;
  attemptTimeoutMs?: number;
}

export function readPurgeEntry(
  tag: string,
  record: TagRecord,
  now: number,
): PurgeEntry | undefined {
  const { stale, expired } = record;
  if (expired !== undefined && expired <= now && (stale === undefined || expired >= stale)) {
    return { tag, mode: "purge", raisedAt: expired };
  }
  if (stale === undefined) return undefined;
  return { tag, mode: "invalidate", raisedAt: stale };
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
  let status: number | undefined;
  for (let attempt = 0; attempt < attempts; attempt++) {
    if (attempt > 0) {
      await pause(random() * Math.min(backoffMs * 2 ** (attempt - 1), backoffCeilingMs));
    }
    try {
      const res = await sender.fetch(url, {
        method: "POST",
        headers: { "content-type": "application/json", [purgeSignatureHeader]: signature },
        body,
        signal: AbortSignal.timeout(sender.attemptTimeoutMs ?? attemptTimeoutMs),
      });
      await res.body?.cancel();
      if (res.ok) return;
      status = res.status;
      last = String(res.status);
      if (!isRetryable(res.status)) break;
    } catch (error) {
      status = undefined;
      last = error instanceof Error ? error.message : String(error);
    }
  }
  throw new PurgeRefusedError(
    `ocel: the purge endpoint ${url} did not take the purge: ${last}`,
    status,
  );
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
