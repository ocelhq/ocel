import {
  DynamoDBClient,
  BatchGetItemCommand,
  UpdateItemCommand,
} from "@aws-sdk/client-dynamodb";
import { S3Client, GetObjectCommand, PutObjectCommand } from "@aws-sdk/client-s3";

// A cache entry exactly as it sits in S3: one object per route holding the html,
// the RSC payload and any PPR segments together, so a read is a single GET and a
// write is atomic. Bodies are base64 so the whole entry stays one JSON document.
export interface CacheEntryFile {
  lastModified: number;
  value: Record<string, any>;
}

// When a tag was last invalidated. Mirrors Next's own tagsManifest entries:
// `expired` marks the moment the tag's content stopped being usable, `stale`
// the moment it should be refreshed in the background.
export interface TagRecord {
  stale?: number;
  expired?: number;
}

// CacheStore is the handler's whole view of its backing services, so the cache
// semantics can be exercised without reaching AWS.
export interface CacheStore {
  readEntry(key: string): Promise<CacheEntryFile | null>;
  writeEntry(key: string, entry: CacheEntryFile): Promise<void>;
  readTags(tags: string[]): Promise<Map<string, TagRecord>>;
  writeTags(tags: string[], record: TagRecord): Promise<void>;
}

function env(name: string): string {
  const value = process.env[name];
  if (!value) throw new Error(`ocel cache handler: ${name} is not set`);
  return value;
}

async function streamToString(body: any): Promise<string> {
  if (typeof body?.transformToString === "function") {
    return body.transformToString();
  }
  const chunks: Buffer[] = [];
  for await (const chunk of body) chunks.push(Buffer.from(chunk));
  return Buffer.concat(chunks).toString("utf8");
}

// awsCacheStore binds the store to the account-global asset bucket and state
// table. Keys are namespaced by the deploy's <env>/<project>/<app>/<build>
// prefix, which is also what the function's IAM policy is scoped to — so a key
// built outside the namespace fails closed rather than reading another app's
// cache.
export function awsCacheStore(): CacheStore {
  const bucket = env("OCEL_ISR_BUCKET");
  const prefix = env("OCEL_ISR_PREFIX");
  const table = env("OCEL_STATE_TABLE");
  const tagNamespace = env("OCEL_ISR_TAG_NAMESPACE");

  const s3 = new S3Client({});
  const ddb = new DynamoDBClient({});

  const objectKey = (key: string) => `${prefix}/cache/${key}.cache.json`;
  const tagPK = (tag: string) => `${tagNamespace}${tag}`;

  return {
    async readEntry(key) {
      try {
        const out = await s3.send(
          new GetObjectCommand({ Bucket: bucket, Key: objectKey(key) }),
        );
        return JSON.parse(await streamToString(out.Body));
      } catch (err: any) {
        if (err?.name === "NoSuchKey" || err?.$metadata?.httpStatusCode === 404) {
          return null;
        }
        throw err;
      }
    },

    async writeEntry(key, entry) {
      await s3.send(
        new PutObjectCommand({
          Bucket: bucket,
          Key: objectKey(key),
          Body: JSON.stringify(entry),
          ContentType: "application/json",
        }),
      );
    },

    async readTags(tags) {
      const found = new Map<string, TagRecord>();
      if (tags.length === 0) return found;

      // BatchGetItem caps at 100 keys per call.
      for (let i = 0; i < tags.length; i += 100) {
        const batch = tags.slice(i, i + 100);
        const out = await ddb.send(
          new BatchGetItemCommand({
            RequestItems: {
              [table]: {
                Keys: batch.map((tag) => ({
                  pk: { S: tagPK(tag) },
                  sk: { S: "#META" },
                })),
              },
            },
          }),
        );
        for (const item of out.Responses?.[table] ?? []) {
          const tag = item.pk?.S?.slice(tagNamespace.length);
          if (!tag) continue;
          found.set(tag, {
            stale: item.stale?.N ? Number(item.stale.N) : undefined,
            expired: item.expired?.N ? Number(item.expired.N) : undefined,
          });
        }
      }
      return found;
    },

    // Merges rather than replaces. Next's own revalidateTag spreads the existing
    // record before applying its updates, so marking a tag stale must not drop an
    // expiry set earlier — a lost `expired` silently makes an invalidated tag
    // look fresh again and resurrects stale content.
    async writeTags(tags, record) {
      const sets: string[] = [];
      const names: Record<string, string> = {};
      const values: Record<string, any> = {};
      for (const field of ["stale", "expired"] as const) {
        const v = record[field];
        if (v === undefined) continue;
        sets.push(`#${field} = :${field}`);
        names[`#${field}`] = field;
        values[`:${field}`] = { N: String(v) };
      }
      if (sets.length === 0) return;

      await Promise.all(
        tags.map((tag) =>
          ddb.send(
            new UpdateItemCommand({
              TableName: table,
              Key: { pk: { S: tagPK(tag) }, sk: { S: "#META" } },
              UpdateExpression: "SET " + sets.join(", "),
              ExpressionAttributeNames: names,
              ExpressionAttributeValues: values,
            }),
          ),
        ),
      );
    },
  };
}
