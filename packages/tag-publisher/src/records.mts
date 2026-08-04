import { isrPrefixOf, mergeRecord, type TagRecord } from "@ocel/next-cache";

// A stream record's NEW_IMAGE, in DynamoDB's own JSON: every value is a
// single-key object naming its type, and numbers travel as strings. Declared
// here rather than imported so this stays the one place the wire shape is
// interpreted.
type Attribute = { S?: string; N?: string };

export interface StreamRecord {
  dynamodb?: {
    SequenceNumber?: string;
    NewImage?: Record<string, Attribute | undefined>;
  };
}

// One build's raise: the records to publish, and the stream records that
// carried them. The sequence numbers are what a build that cannot be published
// is reported back to the event source as, so its batch-mates are acknowledged
// rather than retried and dead-lettered alongside it.
export interface Raise {
  records: Map<string, TagRecord>;
  sequenceNumbers: string[];
}

// Every raise a batch carries, grouped by the build it belongs to. Grouping is
// what turns a batch of N records into one read-merge-write per build rather
// than per record, and the merge is monotone so the order within a build does
// not matter.
export type Raises = Map<string, Raise>;

// A watermark arrives as a string. Anything that is not a finite, non-negative
// number is not one, and is dropped rather than coerced: the merge only ever
// moves a watermark upward, so a value invented here can never be walked back.
function watermark(attr: Attribute | undefined): number | undefined {
  if (attr?.N === undefined) return undefined;
  const value = Number(attr.N);
  return Number.isFinite(value) && value >= 0 ? value : undefined;
}

// raisesOf reads a batch into the raises it carries.
//
// The build comes from gsi1pk, which is the namespace verbatim, and any image
// whose namespace is not one is skipped entirely. That is deliberate belt and
// braces: the event source mapping already filters to the TAG# partitions, but
// a filter is configuration, and this table also holds upload sessions under
// the same sort key with HMAC secrets in them. What this function will not do
// is act on an item it cannot prove is a tag record.
//
// A record with neither watermark raises nothing and is left out — an empty
// record set for a build would publish a snapshot that says nothing changed.
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
