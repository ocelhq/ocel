import { storedCacheTags } from "./cache-tags.mjs";
import { formatStorageCoordinate, type StorageCoordinate } from "./storage-key.mjs";

export const purgePath = "/.ocel/purge";

export const purgeSignatureHeader = "x-ocel-signature";

export const tagsPerPurgeRequest = 500;

const attempts = 4;
const backoffMs = 500;
const backoffCeilingMs = 10_000;
const attemptTimeoutMs = 5_000;

export type PurgeMode = "purge" | "invalidate";

export interface TagRecord {
  stale?: number;
  expired?: number;
}

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

export const purgeHeader = "x-ocel-purge";

export interface PurgeGroup {
  mode: PurgeMode;
  tags: string[];
}

export function formatPurge(groups: readonly PurgeGroup[]): string | undefined {
  const written = groups
    .filter(({ tags }) => tags.length > 0)
    .map(({ mode, tags }) => `${mode} ${tags.join(",")}`);
  return written.length > 0 ? written.join(" ") : undefined;
}

export const purgeHeaderBytes = 4_096;

export function purgeHeaderFor(
  release: string,
  entries: readonly PurgeEntry[],
): string | undefined {
  const groups: PurgeGroup[] = [];
  for (const mode of ["purge", "invalidate"] as PurgeMode[]) {
    const { tags } = storedCacheTags(
      release,
      entries.filter((entry) => entry.mode === mode).map(({ tag }) => tag),
    );
    const kept: PurgeGroup = { mode, tags: [] };
    groups.push(kept);
    for (const tag of tags) {
      kept.tags.push(tag);
      if (formatPurge(groups)!.length > purgeHeaderBytes) {
        kept.tags.pop();
        return formatPurge(groups);
      }
    }
  }
  return formatPurge(groups);
}

export function readPurge(value: string): PurgeGroup[] {
  const words = value.split(/\s+/).filter((word) => word !== "");
  const groups: PurgeGroup[] = [];
  for (let i = 0; i + 1 < words.length; i += 2) {
    const mode = words[i];
    if (mode !== "purge" && mode !== "invalidate") return [];
    groups.push({ mode, tags: (words[i + 1] ?? "").split(",").filter((tag) => tag !== "") });
  }
  return words.length % 2 === 0 ? groups : [];
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

const encoder = new TextEncoder();

function importHmacKey(secret: string, usage: "sign" | "verify") {
  return crypto.subtle.importKey(
    "raw",
    encoder.encode(secret),
    { name: "HMAC", hash: "SHA-256" },
    false,
    [usage],
  );
}

function encodeHex(bytes: ArrayBuffer): string {
  return [...new Uint8Array(bytes)].map((byte) => byte.toString(16).padStart(2, "0")).join("");
}

function decodeHex(hex: string): Uint8Array | undefined {
  if (hex.length === 0 || hex.length % 2 !== 0 || !/^[0-9a-fA-F]+$/.test(hex)) return undefined;
  return Uint8Array.from(hex.match(/../g)!, (pair) => Number.parseInt(pair, 16));
}

export async function signPurge(secret: string, body: string): Promise<string> {
  const key = await importHmacKey(secret, "sign");
  return encodeHex(await crypto.subtle.sign("HMAC", key, encoder.encode(body)));
}

export async function verifyPurge(
  secret: string,
  body: Uint8Array,
  signature: string,
): Promise<boolean> {
  const expected = decodeHex(signature);
  if (!expected) return false;
  const key = await importHmacKey(secret, "verify");
  return crypto.subtle.verify("HMAC", key, expected, body);
}

export function derivePurgeSecret(seed: string, coordinate: StorageCoordinate): Promise<string> {
  return signPurge(seed, formatStorageCoordinate(coordinate));
}

function isRetryable(status: number): boolean {
  return status === 408 || status === 429 || status >= 500;
}

async function post(sender: PurgeSender, url: string, secret: string, body: string): Promise<void> {
  const pause = sender.sleep ?? ((ms: number) => new Promise((done) => setTimeout(done, ms)));
  const random = sender.random ?? Math.random;
  const signature = await signPurge(secret, body);
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
  coordinate: StorageCoordinate,
  entries: readonly PurgeEntry[],
): Promise<void> {
  const storageCoordinate = formatStorageCoordinate(coordinate);
  for (const mode of ["purge", "invalidate"] as const) {
    const tags = entries
      .filter((entry) => entry.mode === mode)
      .map(({ tag, raisedAt }) => ({ tag, raisedAt }));
    for (let i = 0; i < tags.length; i += tagsPerPurgeRequest) {
      const body = JSON.stringify({
        storageCoordinate,
        mode,
        tags: tags.slice(i, i + tagsPerPurgeRequest),
      });
      await post(sender, url, secret, body);
    }
  }
}
