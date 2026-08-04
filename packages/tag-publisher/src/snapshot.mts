import {
  readableSnapshot,
  tagSnapshotKey,
  type StoredTagSnapshot,
  type TagSnapshot,
  type TagSnapshotStore,
} from "@ocel/next-cache";

// The subset of the S3 client this needs, so the store can be driven in a test
// with no AWS and no network.
export interface S3Like {
  send(command: any): Promise<any>;
}

export interface S3Commands {
  GetObjectCommand: new (input: any) => any;
  PutObjectCommand: new (input: any) => any;
}

function isNotFound(err: any): boolean {
  return err?.name === "NoSuchKey" || err?.$metadata?.httpStatusCode === 404;
}

function isPreconditionFailed(err: any): boolean {
  return (
    err?.name === "PreconditionFailed" || err?.$metadata?.httpStatusCode === 412
  );
}

// The publisher's copy of a build's tag clock, in the provider's own bucket.
//
// It is compare-and-swap for the same reason the edge's replica used to be, and
// for the only reason left: an event source mapping has up to two sanctioned
// readers per shard, so two invocations can hold overlapping records for one
// build at once. There is no coordinator on this side — the Durable Object owns
// the edge's copy, not this one — so the object's own version is what serializes
// them. The merge is monotone, so whichever write lands second carries both.
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
    // Throwing rather than reporting absent: replacing a document this reader
    // cannot parse would write away the deploy anchor, and the anchor has
    // exactly one writer — the deploy's genesis seed — so the build would never
    // prune again.
    const snapshot = readableSnapshot(JSON.parse(body) as TagSnapshot);
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
          ...(prior.etag !== null ? { IfMatch: prior.etag } : {}),
        }),
      );
      return true;
    } catch (err) {
      if (isPreconditionFailed(err)) return false;
      throw err;
    }
  }
}
