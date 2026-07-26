import { tagSnapshotKey, type TagSnapshot } from "@ocel/next-cache";
import { createExecutionContext, env } from "cloudflare:test";
import { beforeEach, expect, it } from "vitest";

import {
  CacheEntrypoint,
  createEdgeCache,
  type SnapshotStore,
} from "../src/cache-entrypoint";
import type { Env } from "../src/index";
import type { AwsService } from "../src/signing";

declare module "cloudflare:test" {
  interface ProvidedEnv {
    TAG_SNAPSHOT_STORE: R2Bucket;
  }
}

const region = "eu-west-2";
const bucket = "ocel-assets";
const table = "ocel-state";

// One R2 bucket serves every test, but the snapshot memo is keyed on the store
// *object* — so each test wraps the binding in its own delegate and keeps its
// own isolate-local memo. Real R2 underneath, because whether the republish
// converges is entirely a property of its etag preconditions.
function snapshotStore(hooks: { onGet?: () => Promise<void> } = {}): SnapshotStore {
  return {
    async get(key) {
      const object = await env.TAG_SNAPSHOT_STORE.get(key);
      await hooks.onGet?.();
      return object;
    },
    put: (key, value, options) => env.TAG_SNAPSHOT_STORE.put(key, value, options),
  };
}

interface Call {
  service: AwsService;
  url: string;
  method: string;
  headers: Headers;
  body: string;
}

// A recording stand-in for the signed AWS transport. Signing itself is covered
// by signing.test.ts; what matters here is which call is made, against what, and
// what the entrypoint does with the answer.
function awsRecorder(reply: (call: Call) => Response | Promise<Response> = () => new Response(null, { status: 404 })) {
  const calls: Call[] = [];
  return {
    calls,
    send: async (service: AwsService, url: string, init?: RequestInit) => {
      const call: Call = {
        service,
        url,
        method: init?.method ?? "GET",
        headers: new Headers(init?.headers),
        body:
          typeof init?.body === "string"
            ? init.body
            : init?.body
              ? new TextDecoder().decode(init.body as Uint8Array)
              : "",
      };
      calls.push(call);
      return reply(call);
    },
  };
}

function waitUntilRecorder() {
  const pending: Promise<unknown>[] = [];
  return { pending, waitUntil: (promise: Promise<unknown>) => void pending.push(promise) };
}

const entry = (over: Record<string, unknown> = {}) => ({
  lastModified: 1_000,
  value: { kind: "FETCH", data: { body: "hi" }, ...over },
});

// A fresh scope per test keeps one test's snapshot object out of another's way.
let scopes = 0;
let scope = "";
beforeEach(() => {
  scope = `prod/proj/app/${scopes++}`;
});

async function seedSnapshot(records: TagSnapshot["records"], deployedAt = 0): Promise<void> {
  await env.TAG_SNAPSHOT_STORE.put(
    tagSnapshotKey(scope),
    JSON.stringify({ version: 1, deployedAt, generatedAt: 0, records }),
  );
}

async function storedSnapshot(): Promise<TagSnapshot> {
  const object = await env.TAG_SNAPSHOT_STORE.get(tagSnapshotKey(scope));
  return JSON.parse(await object!.text());
}

function cacheWith(
  aws: ReturnType<typeof awsRecorder>,
  over: { store?: SnapshotStore; waitUntil?: (p: Promise<unknown>) => void; now?: () => number } = {},
) {
  return createEdgeCache({
    region,
    fetchBucket: bucket,
    table,
    aws: aws.send,
    snapshots: over.store ?? snapshotStore(),
    waitUntil: over.waitUntil ?? (() => {}),
    now: over.now ?? (() => 5_000),
  });
}

it("reads a fetch entry from the bucket under the deployment's prefix", async () => {
  const stored = entry();
  const aws = awsRecorder(() => new Response(JSON.stringify(stored)));
  const cache = cacheWith(aws);

  expect(await cache.fetchGet(scope, "abc123", [])).toEqual(stored);
  expect(aws.calls[0]).toMatchObject({ service: "s3", method: "GET" });
  expect(aws.calls[0].url).toBe(
    `https://${bucket}.s3.${region}.amazonaws.com/${scope}/fetch-cache/abc123.cache.json`,
  );
});

it("misses on an absent object", async () => {
  const aws = awsRecorder(() => new Response(null, { status: 404 }));
  expect(await cacheWith(aws).fetchGet(scope, "abc123", [])).toBeNull();
});

it("misses rather than throwing when the store is unreachable", async () => {
  const aws = awsRecorder(() => {
    throw new Error("s3 down");
  });
  expect(await cacheWith(aws).fetchGet(scope, "abc123", [])).toBeNull();
});

it("misses when a tag was invalidated after the entry was written", async () => {
  await seedSnapshot({ posts: { expired: 2_000 } });
  const aws = awsRecorder(() => new Response(JSON.stringify(entry())));
  expect(await cacheWith(aws).fetchGet(scope, "abc123", ["posts"])).toBeNull();
});

it("serves an entry whose tags are clean", async () => {
  await seedSnapshot({ posts: { expired: 500 } });
  const aws = awsRecorder(() => new Response(JSON.stringify(entry())));
  expect(await cacheWith(aws).fetchGet(scope, "abc123", ["posts"])).toEqual(entry());
});

it("evaluates the tags the entry itself recorded, not only the caller's", async () => {
  await seedSnapshot({ authors: { expired: 2_000 } });
  const aws = awsRecorder(() => new Response(JSON.stringify(entry({ tags: ["authors"] }))));
  expect(await cacheWith(aws).fetchGet(scope, "abc123", ["posts"])).toBeNull();
});

it("misses when the tag snapshot cannot be trusted", async () => {
  // No snapshot at all: staleness is unknown, and a data cache has nothing in
  // hand that would make serving anyway the safer answer.
  const aws = awsRecorder(() => new Response(JSON.stringify(entry())));
  expect(await cacheWith(aws).fetchGet(scope, "abc123", ["posts"])).toBeNull();
  // Untagged entries cannot be tag-invalidated, so they are unaffected.
  expect(await cacheWith(aws).fetchGet(scope, "abc123", [])).toEqual(entry());
});

it("returns from a write before the object lands, and writes behind it", async () => {
  let release = () => {};
  const blocked = new Promise<void>((resolve) => (release = resolve));
  const aws = awsRecorder(async (call) => {
    if (call.method === "PUT") await blocked;
    return new Response(null, { status: 200 });
  });
  const { pending, waitUntil } = waitUntilRecorder();

  await cacheWith(aws, { waitUntil }).fetchSet(scope, "abc123", entry(), ["posts"]);

  expect(aws.calls).toHaveLength(1);
  expect(pending).toHaveLength(1);
  release();
  await Promise.all(pending);

  const put = aws.calls[0];
  expect(put.url).toBe(
    `https://${bucket}.s3.${region}.amazonaws.com/${scope}/fetch-cache/abc123.cache.json`,
  );
  // The caller's tags are stamped onto the stored value, which is where the node
  // tier's reader looks for them.
  expect(JSON.parse(put.body)).toEqual({ ...entry(), value: { ...entry().value, tags: ["posts"] } });
});

it("skips an oversized entry rather than failing the render", async () => {
  const aws = awsRecorder();
  const { pending, waitUntil } = waitUntilRecorder();
  const huge = entry({ data: { body: "x".repeat(3 * 1024 * 1024) } });

  await expect(
    cacheWith(aws, { waitUntil }).fetchSet(scope, "abc123", huge, []),
  ).resolves.toBeUndefined();
  expect(aws.calls).toHaveLength(0);
  expect(pending).toHaveLength(0);
});

// Next's node entry templates are bundled into every edge chunk as dead code, so
// a future Next could route a page-kind write through here with no change on our
// side. The bundled client refuses one too, but a build's bundle keeps running
// against a worker deployed long after it — so the invariant has to be enforced
// on the side that cannot be replaced by an old bundle.
it.each(["APP_PAGE", "APP_ROUTE", "PAGES", undefined])(
  "refuses to store a %s entry rather than corrupting the fetch cache",
  async (kind) => {
    const aws = awsRecorder();
    const { pending, waitUntil } = waitUntilRecorder();

    await expect(
      cacheWith(aws, { waitUntil }).fetchSet(scope, "abc123", entry({ kind }), []),
    ).rejects.toThrow(/fetch entries only/);
    expect(aws.calls).toHaveLength(0);
    expect(pending).toHaveLength(0);
  },
);

it("does not surface a failed write to the caller", async () => {
  const aws = awsRecorder(() => {
    throw new Error("s3 down");
  });
  const { pending, waitUntil } = waitUntilRecorder();
  await cacheWith(aws, { waitUntil }).fetchSet(scope, "abc123", entry(), []);
  await expect(Promise.all(pending)).resolves.toBeDefined();
});

it("records one tag update per tag, under the prefix's TAG# namespace", async () => {
  const aws = awsRecorder(() => new Response("{}"));
  await cacheWith(aws).revalidateTags(scope, ["posts", "authors"]);

  const updates = aws.calls.filter((call) => call.service === "dynamodb");
  expect(updates).toHaveLength(2);
  expect(updates[0].url).toBe(`https://dynamodb.${region}.amazonaws.com/`);
  expect(updates[0].headers.get("x-amz-target")).toBe("DynamoDB_20120810.UpdateItem");
  const body = JSON.parse(updates[0].body);
  expect(body.TableName).toBe(table);
  expect(body.Key.pk.S).toBe(`TAG#${scope.replaceAll("/", "#")}#posts`);
  expect(body.ExpressionAttributeValues[":expired"].N).toBe("5000");
});

it("marks tags stale now and dead at the end of an expire window", async () => {
  const aws = awsRecorder(() => new Response("{}"));
  await cacheWith(aws).revalidateTags(scope, ["posts"], { expire: 60 });

  const values = JSON.parse(aws.calls[0].body).ExpressionAttributeValues;
  expect(values[":stale"].N).toBe("5000");
  expect(values[":expired"].N).toBe(String(5_000 + 60_000));
});

it("treats a rejected guard as the ordinary outcome it is", async () => {
  const aws = awsRecorder((call) =>
    call.service === "dynamodb"
      ? new Response(
          JSON.stringify({
            __type: "com.amazonaws.dynamodb.v20120810#ConditionalCheckFailedException",
          }),
          { status: 400 },
        )
      : new Response("{}"),
  );
  await expect(cacheWith(aws).revalidateTags(scope, ["posts"])).resolves.toBeUndefined();
  // The replica is still republished: the winning writer's record is the one
  // that has to reach the edge.
  expect((await storedSnapshot()).records.posts).toBeDefined();
});

it("surfaces a tag write that failed for any other reason", async () => {
  const aws = awsRecorder(() => new Response(JSON.stringify({ __type: "x#ThrottlingException" }), { status: 400 }));
  await expect(cacheWith(aws).revalidateTags(scope, ["posts"])).rejects.toThrow(/dynamodb 400/);
});

it("merges the invalidation into the published snapshot", async () => {
  await seedSnapshot({ authors: { expired: 4_000 } }, 100);
  const aws = awsRecorder(() => new Response("{}"));
  await cacheWith(aws).revalidateTags(scope, ["posts"]);

  const snapshot = await storedSnapshot();
  expect(snapshot).toMatchObject({ version: 1, deployedAt: 100, generatedAt: 5_000 });
  expect(snapshot.records.posts?.expired).toBe(5_000);
  // The deploy's anchor and another writer's record both survive the merge.
  expect(snapshot.records.authors?.expired).toBe(4_000);
});

it("converges when another publisher lands between the read and the write", async () => {
  await seedSnapshot({}, 100);
  let raced = false;
  const store = snapshotStore({
    onGet: async () => {
      if (raced) return;
      raced = true;
      await env.TAG_SNAPSHOT_STORE.put(
        tagSnapshotKey(scope),
        JSON.stringify({
          version: 1,
          deployedAt: 100,
          generatedAt: 4_000,
          records: { authors: { expired: 4_000 } },
        }),
      );
    },
  });
  const aws = awsRecorder(() => new Response("{}"));
  await cacheWith(aws, { store }).revalidateTags(scope, ["posts"]);

  // The losing writer retried from the winner's document, so neither
  // invalidation is lost.
  const snapshot = await storedSnapshot();
  expect(snapshot.records.posts?.expired).toBe(5_000);
  expect(snapshot.records.authors?.expired).toBe(4_000);
});

it("does not clobber a replica another publisher created first", async () => {
  let raced = false;
  const store = snapshotStore({
    onGet: async () => {
      if (raced) return;
      raced = true;
      await env.TAG_SNAPSHOT_STORE.put(
        tagSnapshotKey(scope),
        JSON.stringify({
          version: 1,
          deployedAt: 100,
          generatedAt: 4_000,
          records: { authors: { expired: 4_000 } },
        }),
      );
    },
  });
  const aws = awsRecorder(() => new Response("{}"));
  await cacheWith(aws, { store }).revalidateTags(scope, ["posts"]);

  // The genesis write is conditional on the object not existing, so it loses to
  // the publisher that got there first and retries from that document.
  const snapshot = await storedSnapshot();
  expect(snapshot.deployedAt).toBe(100);
  expect(snapshot.records.posts?.expired).toBe(5_000);
  expect(snapshot.records.authors?.expired).toBe(4_000);
});

// Next calls revalidateTag with no try/catch of its own, and a Server Action
// awaits the drain before responding — so a throw out of the replication half
// fails the very route that raised the invalidation. The durable record is
// already written by then; the replica is the edge's copy of it, and a copy that
// cannot be replaced leaves the edge reading the last one.
it("leaves a replica it cannot read alone, without failing the route", async () => {
  await env.TAG_SNAPSHOT_STORE.put(tagSnapshotKey(scope), "{{ not json");
  const aws = awsRecorder(() => new Response("{}"));

  await expect(cacheWith(aws).revalidateTags(scope, ["posts"])).resolves.toBeUndefined();

  // The durable write still happened: the invalidation is recorded even though
  // the replica could not be republished.
  expect(aws.calls.filter((call) => call.service === "dynamodb")).toHaveLength(1);
  expect(await (await env.TAG_SNAPSHOT_STORE.get(tagSnapshotKey(scope)))!.text()).toBe("{{ not json");
});

it("does not fail the route when every conditional write is lost", async () => {
  await seedSnapshot({}, 100);
  const store = snapshotStore();
  const aws = awsRecorder(() => new Response("{}"));

  await expect(
    cacheWith(aws, { store: { ...store, put: async () => null } }).revalidateTags(scope, ["posts"]),
  ).resolves.toBeUndefined();

  expect(aws.calls.filter((call) => call.service === "dynamodb")).toHaveLength(1);
});

it("sees its own invalidation on the very next read", async () => {
  const stored = entry();
  const aws = awsRecorder((call) =>
    call.service === "s3" ? new Response(JSON.stringify(stored)) : new Response("{}"),
  );
  const cache = cacheWith(aws, { store: snapshotStore() });

  // Warms this isolate's memo with the pre-invalidation snapshot.
  await seedSnapshot({ posts: { expired: 500 } });
  expect(await cache.fetchGet(scope, "abc123", ["posts"])).toEqual(stored);

  await cache.revalidateTags(scope, ["posts"]);
  expect(await cache.fetchGet(scope, "abc123", ["posts"])).toBeNull();
});

// The worker script is frozen and outlives its deployments (ADR 0002), so it can
// find itself on a substrate whose bootstrap predates the stores its cache reads:
// the region, table and bucket vars are then simply unbound. Every call must
// answer like an empty cache, because a throw would take down the edge render
// that made it rather than leaving that render uncached.
it("answers like an empty cache on a substrate that binds no coordinates", async () => {
  const entrypoint = new CacheEntrypoint(createExecutionContext(), {} as Env);

  expect(await entrypoint.fetchGet(scope, "abc123", ["posts"])).toBeNull();
  await entrypoint.fetchSet(scope, "abc123", entry(), ["posts"]);
  await entrypoint.revalidateTags(scope, ["posts"]);
});
