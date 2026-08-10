import { isrPrefixOf, mergeRecord, type TagRecord } from "@framework/next-cache";

type Attribute = { S?: string; N?: string };

export interface StreamRecord {
  dynamodb?: {
    SequenceNumber?: string;
    NewImage?: Record<string, Attribute | undefined>;
  };
}

export interface Raise {
  records: Map<string, TagRecord>;
  sequenceNumbers: string[];
}

export type Raises = Map<string, Raise>;

function watermark(attr: Attribute | undefined): number | undefined {
  if (attr?.N === undefined) return undefined;
  const value = Number(attr.N);
  return Number.isFinite(value) && value >= 0 ? value : undefined;
}

export function raisesOf(records: readonly StreamRecord[]): Raises {
  const raises: Raises = new Map();
  for (const record of records) {
    const image = record.dynamodb?.NewImage;
    if (image === undefined) continue;

    const isrPrefix = isrPrefixOf(image.gsi1pk?.S ?? "");
    const tag = image.tag?.S;
    if (isrPrefix === null || tag === undefined || tag === "") continue;

    const stale = watermark(image.stale);
    const expired = watermark(image.expired);
    if (stale === undefined && expired === undefined) continue;

    const build: Raise = raises.get(isrPrefix) ?? { records: new Map(), sequenceNumbers: [] };
    const incoming = {
      ...(stale !== undefined ? { stale } : {}),
      ...(expired !== undefined ? { expired } : {}),
    };
    build.records.set(tag, mergeRecord(build.records.get(tag), incoming));
    if (record.dynamodb?.SequenceNumber !== undefined) {
      build.sequenceNumbers.push(record.dynamodb.SequenceNumber);
    }
    raises.set(isrPrefix, build);
  }
  return raises;
}
