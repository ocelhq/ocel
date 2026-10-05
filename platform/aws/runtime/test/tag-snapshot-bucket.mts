import { vi } from "vitest";

interface StoredObject {
  body: string;
  etag: string;
}

export type TagSnapshotBucket = ReturnType<typeof newTagSnapshotBucket>;

export function newTagSnapshotBucket(snapshotKey: string, seed: Record<string, unknown> | null) {
  const objects = new Map<string, StoredObject>();
  let version = 0;
  const put = (key: string, body: string) => {
    version++;
    objects.set(key, { body, etag: `"v${version}"` });
  };
  if (seed) put(snapshotKey, JSON.stringify(seed));
  let losses = 0;
  let conflicts = 0;
  return {
    objects,
    loseNextWrites(count: number) {
      losses = count;
    },
    conflictNextWrites(count: number) {
      conflicts = count;
    },
    readRecords(): Record<string, unknown> {
      return JSON.parse(objects.get(snapshotKey)!.body).records;
    },
    async send(cmd: any) {
      const { Key, IfMatch, Body } = cmd.input;
      const stored = objects.get(Key);
      if (cmd.constructor.name === "PutObjectCommand" && conflicts > 0) {
        conflicts--;
        throw Object.assign(new Error("conflict"), {
          name: "ConditionalRequestConflict",
          $metadata: { httpStatusCode: 409 },
        });
      }
      if (cmd.constructor.name === "GetObjectCommand") {
        if (!stored) throw Object.assign(new Error("missing"), { name: "NoSuchKey" });
        return { Body: { transformToString: async () => stored.body }, ETag: stored.etag };
      }
      if (losses > 0 || (IfMatch !== undefined && IfMatch !== stored?.etag)) {
        losses = Math.max(0, losses - 1);
        put(Key, stored?.body ?? "{}");
        throw Object.assign(new Error("precondition"), { name: "PreconditionFailed" });
      }
      put(Key, Body);
      return {};
    },
  };
}

export function serveS3From(bucket: () => TagSnapshotBucket): void {
  vi.doMock("@aws-sdk/client-s3", async (orig) => {
    const actual = await orig<any>();
    return {
      ...actual,
      S3Client: class {
        send(cmd: any) {
          return bucket().send(cmd);
        }
      },
    };
  });
}
