import { isrPrefixOf } from "@framework/next-cache";

type Attribute = { S?: string; N?: string };

export interface StreamRecord {
  dynamodb?: {
    SequenceNumber?: string;
    NewImage?: Record<string, Attribute | undefined>;
  };
}

export interface Raise {
  tags: string[];
  sequenceNumbers: string[];
}

export type Raises = Map<string, Raise>;

export interface Coordinate {
  project: string;
  release: string;
}

function raised(attr: Attribute | undefined): boolean {
  if (attr?.N === undefined) return false;
  const value = Number(attr.N);
  return Number.isFinite(value) && value >= 0;
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
    if (!raised(image.stale) && !raised(image.expired)) continue;

    const raise: Raise = raises.get(isrPrefix) ?? { tags: [], sequenceNumbers: [] };
    if (!raise.tags.includes(tag)) raise.tags.push(tag);
    if (record.dynamodb?.SequenceNumber !== undefined) {
      raise.sequenceNumbers.push(record.dynamodb.SequenceNumber);
    }
    raises.set(isrPrefix, raise);
  }
  return raises;
}
