import type { AttributeValue, UpdateItemCommandInput } from "@aws-sdk/client-dynamodb";

// A tag record as it is written: the invalidation watermarks plus the time the
// write happened, which is what the index is sorted by. The two are separate
// because a sync has to find changed records regardless of what value they carry.
export interface TagRecordUpdate {
  stale?: number;
  expired?: number;
  writtenAt: number;
}

// The index sort key is a string attribute, so a timestamp has to be padded to a
// fixed width for lexicographic ordering to match numeric ordering. 15 digits
// outlasts millisecond epochs by several millennia.
const sortKeyWidth = 15;

// Rounded here rather than at the call site: the clock's timestamps come from
// performance.now(), which is fractional, and a fractional string neither pads
// to the fixed width nor orders lexicographically against one that does.
export const tagSortKey = (at: number) =>
  String(Math.round(at)).padStart(sortKeyWidth, "0");

// Both cache tiers write the same tag record, so the update is built here rather
// than twice. A reader of the index cannot tell which tier wrote a row, so any
// drift between them — a sort key encoded differently, a row left unindexed, a
// guard applied on one side only — corrupts the index rather than one writer.
//
// The guard is monotonic on the field being advanced, which is a watermark
// meaning everything created at or before this instant is dead: a larger value
// is strictly stricter, so rejecting a smaller write can only over-invalidate.
// Rejection is a *common* path, because both tiers raise every invalidation and
// the second write for an event always loses.
//
// Only the fields the event actually carries are written, and the guard is on
// the field being advanced. Writing an absent field as 0 would both clobber a
// value another instance set and — since the guard is a strict `<` — wedge the
// record, so every later write of that field is rejected against its own zero.
export function tagRecordUpdate(
  table: string,
  namespace: string,
  tag: string,
  record: TagRecordUpdate,
): UpdateItemCommandInput {
  const advancing = record.expired !== undefined ? "expired" : "stale";
  const sets = ["tag = :tag", "gsi1pk = :ns", "gsi1sk = :writtenAt"];
  const values: Record<string, AttributeValue> = {
    ":tag": { S: tag },
    ":ns": { S: namespace },
    ":writtenAt": { S: tagSortKey(record.writtenAt) },
  };
  for (const field of ["expired", "stale"] as const) {
    const value = record[field];
    if (value === undefined) continue;
    sets.push(`${field} = :${field}`);
    values[`:${field}`] = { N: String(value) };
  }

  return {
    TableName: table,
    Key: { pk: { S: `${namespace}${tag}` }, sk: { S: "#META" } },
    ConditionExpression: `attribute_not_exists(${advancing}) OR ${advancing} < :${advancing}`,
    UpdateExpression: "SET " + sets.join(", "),
    ExpressionAttributeValues: values,
  };
}

export function isGuardRejection(err: any): boolean {
  return err?.name === "ConditionalCheckFailedException";
}
