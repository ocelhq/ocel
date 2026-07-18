import { DynamoDBClient, QueryCommand, UpdateItemCommand } from "@aws-sdk/client-dynamodb";

// A tag record as it is written: the invalidation watermarks plus the time the
// write happened, which is what the index is sorted by. The two are separate
// because a sync has to find changed records regardless of what value they carry.
export interface TagRecordUpdate {
  stale?: number;
  expired?: number;
  writtenAt: number;
}

export interface TagRecordRow extends TagRecordUpdate {
  tag: string;
}

// `cursor` is DynamoDB's LastEvaluatedKey, opaque to the caller: present only
// when the page was truncated, and handed straight back to read the next one.
export interface TagRecordPage {
  records: TagRecordRow[];
  cursor?: unknown;
}

// UseCacheStore is the plural cache handlers' whole view of their backing
// services, so the cache semantics can be exercised without reaching AWS.
export interface UseCacheStore {
  queryTagRecords(since: number, cursor?: unknown): Promise<TagRecordPage>;
  writeTag(tag: string, record: TagRecordUpdate): Promise<boolean>;
}

// The index sort key is a string attribute, so a timestamp has to be padded to a
// fixed width for lexicographic ordering to match numeric ordering. 15 digits
// outlasts millisecond epochs by several millennia.
const sortKeyWidth = 15;

const sortKey = (at: number) => String(at).padStart(sortKeyWidth, "0");

// One page is deliberately large: a cold instance drains the whole partition,
// and every extra round trip is one more on its first request.
const tagPageSize = 200;

function env(name: string): string {
  const value = process.env[name];
  if (!value) throw new Error(`ocel use cache: ${name} is not set`);
  return value;
}

// awsUseCacheStore binds the store to the account-global state table. Tag keys
// are namespaced by the deploy, which is also what the function's IAM policy is
// scoped to — including on the index, whose partition key must therefore be the
// namespace exactly as granted or the query fails closed with a 403.
export function awsUseCacheStore(): UseCacheStore {
  const table = env("OCEL_STATE_TABLE");
  const tagNamespace = env("OCEL_ISR_TAG_NAMESPACE");
  const index = env("OCEL_STATE_TABLE_INDEX");

  const ddb = new DynamoDBClient({});

  return {
    async queryTagRecords(since, cursor) {
      const out = await ddb.send(
        new QueryCommand({
          TableName: table,
          IndexName: index,
          KeyConditionExpression: "gsi1pk = :ns AND gsi1sk > :since",
          ExpressionAttributeValues: {
            ":ns": { S: tagNamespace },
            ":since": { S: sortKey(since) },
          },
          Limit: tagPageSize,
          ExclusiveStartKey: cursor as Record<string, any> | undefined,
        }),
      );

      const records: TagRecordRow[] = [];
      for (const item of out.Items ?? []) {
        // The tag is an explicit attribute rather than a slice of the partition
        // key, so a reader never has to know the namespace it was written under.
        const tag = item.tag?.S;
        if (!tag) continue;
        records.push({
          tag,
          stale: item.stale?.N ? Number(item.stale.N) : undefined,
          expired: item.expired?.N ? Number(item.expired.N) : undefined,
          writtenAt: Number(item.gsi1sk?.S ?? 0),
        });
      }
      return { records, cursor: out.LastEvaluatedKey };
    },

    // Writes into the same record the incremental cache's tag store already
    // uses: revalidateTag fans out to both, so both clocks observe every event.
    //
    // The guard is monotonic on `expired`, which is a watermark meaning
    // everything created at or before this instant is dead — a larger value is
    // strictly stricter, so rejecting a smaller write can only over-invalidate.
    // Rejection is the *common* path, because Next calls updateTags on every
    // registered handler and the second write for an event always loses.
    async writeTag(tag, record) {
      try {
        await ddb.send(
          new UpdateItemCommand({
            TableName: table,
            Key: { pk: { S: `${tagNamespace}${tag}` }, sk: { S: "#META" } },
            ConditionExpression: "attribute_not_exists(expired) OR expired < :expired",
            UpdateExpression:
              "SET expired = :expired, stale = :stale, tag = :tag, gsi1pk = :ns, gsi1sk = :writtenAt",
            ExpressionAttributeValues: {
              // A record with no expiry at all writes 0, which is below every
              // real timestamp and so never invalidates anything. Next only
              // reaches that state by passing durations without an expire
              // window, which its own cacheLife profiles do not do.
              ":expired": { N: String(record.expired ?? 0) },
              ":stale": { N: String(record.stale ?? 0) },
              ":tag": { S: tag },
              ":ns": { S: tagNamespace },
              ":writtenAt": { S: sortKey(record.writtenAt) },
            },
          }),
        );
        return true;
      } catch (err: any) {
        if (err?.name === "ConditionalCheckFailedException") return false;
        throw err;
      }
    },
  };
}
