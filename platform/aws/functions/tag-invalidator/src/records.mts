import { isrPrefixOf } from "@platform/aws-runtime/tag-index";

import type { PurgeEntry } from "@platform/edge-contract/purge";

type Attribute = { S?: string; N?: string };

export interface StreamRecord {
  dynamodb?: {
    SequenceNumber?: string;
    NewImage?: Record<string, Attribute | undefined>;
  };
}

export interface Raise {
  tags: string[];
  entries: PurgeEntry[];
  sequenceNumbers: string[];
}

export type Raises = Map<string, Raise>;

export interface Coordinate {
  project: string;
  release: string;
}

function watermark(attr: Attribute | undefined): number | undefined {
  if (attr?.N === undefined) return undefined;
  const value = Number(attr.N);
  return Number.isFinite(value) && value >= 0 ? value : undefined;
}

function entryOf(
  tag: string,
  stale: number | undefined,
  expired: number | undefined,
): PurgeEntry | undefined {
  const raisedAt = stale ?? expired;
  if (raisedAt === undefined) return undefined;
  return {
    tag,
    mode: expired !== undefined && expired <= raisedAt ? "purge" : "invalidate",
    raisedAt,
  };
}

function merged(entries: PurgeEntry[], next: PurgeEntry): void {
  const i = entries.findIndex((entry) => entry.tag === next.tag);
  const known = entries[i];
  if (known === undefined) {
    entries.push(next);
    return;
  }
  entries[i] = {
    tag: next.tag,
    mode: known.mode === "purge" || next.mode === "purge" ? "purge" : "invalidate",
    raisedAt: Math.max(known.raisedAt, next.raisedAt),
  };
}

export function coordinateOf(isrPrefix: string): Coordinate | null {
  const segments = isrPrefix.split("/");
  if (segments.length !== 5) return null;
  return { project: segments[1]!, release: segments[3]! };
}

export function raisesOf(records: readonly StreamRecord[]): Raises {
  const raises: Raises = new Map();
  for (const record of records) {
    const image = record.dynamodb?.NewImage;
    if (image === undefined) continue;

    const isrPrefix = isrPrefixOf(image.gsi1pk?.S ?? "");
    const tag = image.tag?.S;
    if (isrPrefix === null || tag === undefined || tag === "") continue;
    const entry = entryOf(tag, watermark(image.stale), watermark(image.expired));
    if (entry === undefined) continue;

    const raise: Raise = raises.get(isrPrefix) ?? { tags: [], entries: [], sequenceNumbers: [] };
    if (!raise.tags.includes(tag)) raise.tags.push(tag);
    merged(raise.entries, entry);
    if (record.dynamodb?.SequenceNumber !== undefined) {
      raise.sequenceNumbers.push(record.dynamodb.SequenceNumber);
    }
    raises.set(isrPrefix, raise);
  }
  return raises;
}
