import { GetObjectCommand, PutObjectCommand } from "@aws-sdk/client-s3";
import {
  newTagPublisher,
  type PublishTag,
  readableSnapshot,
  type StoredTagSnapshot,
  type TagSnapshot,
  type TagSnapshotStore,
  tagSnapshotKey,
} from "@framework/next-cache";
import { isNotFound, requireEnv } from "./object-store.mjs";
import { newS3Client } from "./sdk-clients.mjs";

export interface S3Like {
  send(command: any): Promise<any>;
}

export interface S3Commands {
  GetObjectCommand: new (input: any) => any;
  PutObjectCommand: new (input: any) => any;
}

function isLostVersion(err: any): boolean {
  const status = err?.$metadata?.httpStatusCode;
  return (
    err?.name === "PreconditionFailed" ||
    err?.name === "ConditionalRequestConflict" ||
    status === 412 ||
    status === 409
  );
}

export class S3TagSnapshotStore implements TagSnapshotStore {
  constructor(
    private readonly s3: S3Like,
    private readonly commands: S3Commands,
    private readonly bucket: string,
    private readonly isrPrefix: string,
  ) {}

  private get key(): string {
    return tagSnapshotKey(this.isrPrefix);
  }

  async read(): Promise<StoredTagSnapshot | null> {
    let response;
    try {
      response = await this.s3.send(
        new this.commands.GetObjectCommand({ Bucket: this.bucket, Key: this.key }),
      );
    } catch (err) {
      if (isNotFound(err)) return null;
      throw err;
    }
    const body = await response.Body.transformToString();
    const snapshot = readableSnapshot(JSON.parse(body));
    if (snapshot === null) {
      throw new Error(`tag snapshot ${this.key} is not a version this publisher can merge into`);
    }
    return { snapshot, etag: response.ETag ?? null };
  }

  async write(snapshot: TagSnapshot, prior: StoredTagSnapshot): Promise<boolean> {
    try {
      await this.s3.send(
        new this.commands.PutObjectCommand({
          Bucket: this.bucket,
          Key: this.key,
          Body: JSON.stringify(snapshot),
          ContentType: "application/json",
          IfMatch: prior.etag,
        }),
      );
      return true;
    } catch (err) {
      if (isLostVersion(err)) return false;
      throw err;
    }
  }
}

export function newAwsTagPublisher(): PublishTag {
  return newTagPublisher(
    new S3TagSnapshotStore(
      newS3Client(),
      { GetObjectCommand, PutObjectCommand },
      requireEnv("OCEL_ISR_BUCKET"),
      requireEnv("OCEL_ISR_PREFIX"),
    ),
  );
}
