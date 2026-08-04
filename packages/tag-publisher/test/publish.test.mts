import { tagSnapshotKey, type TagRecord, type TagSnapshot } from "@ocel/next-cache";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { publishAll } from "../src/publish.mjs";
import { isrWriteSecret } from "../src/writer.mjs";
import type { Raises } from "../src/records.mjs";

const PREFIX = "prod/acme/web/BUILD1";
const KEY = tagSnapshotKey(PREFIX);
const SEED = "seed-1";

// A minimal S3 whose objects carry an etag, so the compare-and-swap the
// publisher runs against a second reader of the same shard is exercised rather
// than assumed.
class FakeS3 {
  objects = new Map<string, { body: string; etag: string }>();
  puts: any[] = [];
  private version = 0;

  async send(command: any): Promise<any> {
    const { kind, input } = command;
    if (kind === "get") {
      const object = this.objects.get(input.Key);
      if (object === undefined) {
        throw Object.assign(new Error("NoSuchKey"), { name: "NoSuchKey" });
      }
      return { ETag: object.etag, Body: { transformToString: async () => object.body } };
    }
    this.puts.push(input);
    const existing = this.objects.get(input.Key);
    const lost =
      (input.IfNoneMatch === "*" && existing !== undefined) ||
      (input.IfMatch !== undefined && input.IfMatch !== existing?.etag);
    if (lost) {
      throw Object.assign(new Error("PreconditionFailed"), { name: "PreconditionFailed" });
    }
    this.objects.set(input.Key, { body: input.Body, etag: `v${++this.version}` });
    return {};
  }
}

const commands = {
  GetObjectCommand: class {
    kind = "get";
    constructor(public input: any) {}
  },
  PutObjectCommand: class {
    kind = "put";
    constructor(public input: any) {}
  },
} as any;

function raises(records: Record<string, TagRecord>): Raises {
  return new Map([[PREFIX, new Map(Object.entries(records))]]);
}

function publisher(s3: FakeS3, fetchImpl: any) {
  return {
    s3,
    commands,
    fetch: fetchImpl,
    assetBucket: "assets",
    endpoint: "https://writer.example",
    seed: SEED,
  };
}

function stored(s3: FakeS3): TagSnapshot {
  return JSON.parse(s3.objects.get(KEY)!.body) as TagSnapshot;
}

describe("publishAll", () => {
  let ok: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    ok = vi.fn(async () => new Response(null, { status: 204 }));
  });

  it("merges into the deploy's genesis rather than replacing it", async () => {
    const s3 = new FakeS3();
    s3.objects.set(KEY, {
      body: JSON.stringify({ version: 1, deployedAt: 10, generatedAt: 10, records: {} }),
      etag: "v0",
    });

    await publishAll(publisher(s3, ok), raises({ cart: { expired: 500 } }), 900);

    const snapshot = stored(s3);
    // The anchor is the deploy's and has no other writer: losing it would
    // disable pruning for the life of the build.
    expect(snapshot.deployedAt).toBe(10);
    expect(snapshot.generatedAt).toBe(900);
    expect(snapshot.records.cart).toEqual({ stale: undefined, expired: 500 });
  });

  it("raises to the build's own path under the secret its Lambdas hold", async () => {
    await publishAll(publisher(new FakeS3(), ok), raises({ cart: { expired: 5 } }), 1);

    const [url, init] = ok.mock.calls[0]!;
    expect(url).toBe(`https://writer.example/${PREFIX}/tags`);
    expect(init.headers.authorization).toBe(`Bearer ${isrWriteSecret(SEED, PREFIX)}`);
    expect(JSON.parse(init.body)).toEqual({ records: { cart: { expired: 5 } } });
  });

  it("is idempotent: replaying a batch converges on the same document", async () => {
    const s3 = new FakeS3();
    const p = publisher(s3, ok);
    await publishAll(p, raises({ cart: { expired: 500 } }), 1);
    await publishAll(p, raises({ cart: { expired: 500 } }), 2);
    await publishAll(p, raises({ cart: { expired: 200 } }), 3);

    expect(stored(s3).records.cart).toEqual({ stale: undefined, expired: 500 });
  });

  it("fails the batch when the writer will not take the raise", async () => {
    // 429 means nothing durable happened and the records are still in hand, so
    // the batch is retried rather than acknowledged.
    const exhausted = vi.fn(async () => new Response(null, { status: 429 }));
    await expect(
      publishAll(publisher(new FakeS3(), exhausted), raises({ cart: { expired: 5 } }), 1),
    ).rejects.toThrow();
  });

  it("attempts every build before failing the batch", async () => {
    const s3 = new FakeS3();
    const other = "prod/acme/admin/BUILD2";
    const fetchImpl = vi.fn(async (url: string) =>
      url.includes(other) ? new Response(null, { status: 500 }) : new Response(null, { status: 204 }),
    );
    const both: Raises = new Map([
      [PREFIX, new Map([["cart", { expired: 5 }]])],
      [other, new Map([["home", { expired: 6 }]])],
    ]);

    await expect(publishAll(publisher(s3, fetchImpl), both, 1)).rejects.toThrow();
    expect(s3.objects.has(KEY)).toBe(true);
  });

  it("refuses to write over a document it cannot read", async () => {
    // The version it does not know may carry the deploy anchor somewhere else,
    // and clobbering that is unbounded where declining costs one build.
    const s3 = new FakeS3();
    s3.objects.set(KEY, { body: JSON.stringify({ version: 2, records: {} }), etag: "v0" });

    await expect(
      publishAll(publisher(s3, ok), raises({ cart: { expired: 5 } }), 1),
    ).rejects.toThrow();
    expect(s3.puts).toEqual([]);
    expect(stored(s3).version).toBe(2);
  });
});
