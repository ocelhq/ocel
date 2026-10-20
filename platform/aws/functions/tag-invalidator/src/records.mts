import { parseTagNamespace } from "@platform/aws-runtime/tag-index";

import { type PurgeEntry, readPurgeEntry } from "@platform/edge-contract/purge";
import { pagesPrefix, parseObjectKey } from "@platform/edge-contract/storage-key";

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

function readTimestamp(attr: Attribute | undefined): number | undefined {
  if (attr?.N === undefined) return undefined;
  const value = Number(attr.N);
  return Number.isFinite(value) && value >= 0 ? value : undefined;
}

function readWrittenAt(attr: Attribute | undefined): number | undefined {
  if (attr?.S === undefined || !/^[0-9]+$/.test(attr.S)) return undefined;
  return Number(attr.S);
}

function isCoveredBy(other: PurgeEntry, entry: PurgeEntry): boolean {
  return (
    entry.tag === other.tag &&
    (entry.mode === "purge" || entry.mode === other.mode) &&
    entry.raisedAt >= other.raisedAt
  );
}

function mergeEntry(entries: PurgeEntry[], next: PurgeEntry): void {
  if (entries.some((entry) => isCoveredBy(next, entry))) return;
  const kept = entries.filter((entry) => !isCoveredBy(entry, next));
  entries.splice(0, entries.length, ...kept, next);
}

export function coordinateOf(prefix: string): Coordinate | null {
  const key = parseObjectKey(prefix);
  if (key?.kind !== "pages" || key.rest !== "") return null;
  return { project: key.project, release: key.release };
}

export function raisesOf(records: readonly StreamRecord[]): Raises {
  const raises: Raises = new Map();
  for (const record of records) {
    const image = record.dynamodb?.NewImage;
    if (image === undefined) continue;

    const coordinate = parseTagNamespace(image.gsi1pk?.S ?? "");
    const tag = image.tag?.S;
    if (coordinate === null || tag === undefined || tag === "") continue;
    const prefix = pagesPrefix(coordinate);
    const entry = readPurgeEntry(
      tag,
      { stale: readTimestamp(image.stale), expired: readTimestamp(image.expired) },
      readWrittenAt(image.gsi1sk) ?? Date.now(),
    );
    if (entry === undefined) continue;

    const raise: Raise = raises.get(prefix) ?? { tags: [], entries: [], sequenceNumbers: [] };
    if (!raise.tags.includes(tag)) raise.tags.push(tag);
    mergeEntry(raise.entries, entry);
    if (record.dynamodb?.SequenceNumber !== undefined) {
      raise.sequenceNumbers.push(record.dynamodb.SequenceNumber);
    }
    raises.set(prefix, raise);
  }
  return raises;
}
