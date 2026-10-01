import { parseDurationMilliseconds } from "../delivery/duration.js";

/** A length of time written with its unit, such as `"500ms"`, `"10s"`, `"5m"`, `"2h"` or `"30d"`. */
export type KVDuration = `${number}ms` | `${number}s` | `${number}m` | `${number}h` | `${number}d`;

/**
 * How long a write leaves its key to live: a duration replaces the TTL, `"keep"` keeps the
 * one the key has, and `null` clears it so the key lives until it is deleted or evicted.
 */
export type WriteTTL = KVDuration | "keep" | null;

/** How one write treats its key's TTL. */
export interface WriteOptions {
  /** The TTL this write sets instead of the entry's own; without it, the entry's own. */
  ttl?: WriteTTL;
}

export type TTL = { kind: "expire"; milliseconds: number } | { kind: "keep" } | { kind: "clear" };

export function parseTTL(duration: unknown): number {
  if (typeof duration !== "string") {
    throw new Error(
      `a ttl is a duration written with its unit, such as "10s", "5m" or "30d", not ${String(duration)}`,
    );
  }
  const milliseconds = Math.round(parseDurationMilliseconds(duration as KVDuration));
  if (milliseconds < 1) {
    throw new Error(`a ttl is at least 1ms, and "${duration}" is shorter`);
  }
  return milliseconds;
}

export function resolveTTL(declared: number | undefined, written: WriteTTL | undefined): TTL {
  if (written === undefined) {
    return declared === undefined ? { kind: "clear" } : { kind: "expire", milliseconds: declared };
  }
  if (written === null) return { kind: "clear" };
  if (written === "keep") return { kind: "keep" };
  return { kind: "expire", milliseconds: parseTTL(written) };
}
