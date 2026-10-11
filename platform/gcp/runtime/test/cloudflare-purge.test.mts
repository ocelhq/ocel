import { createHmac } from "node:crypto";

import type { TagRecord } from "@framework/next-cache";
import { purgeSignatureHeader } from "@platform/edge-contract/purge";
import { expect, test } from "vitest";

import { withCdnPurge } from "../src/next/cdn-purge.mjs";
import { newCloudflarePurge } from "../src/next/cloudflare-purge.mjs";

const url = "https://shop-prod.acct.workers.dev/.ocel/purge";
const isrPrefix = "prod/shop/web/r1a2b3c4d/isr";
const secret = "write-secret";
const raisedAt = 1_700_000_000_000;

type Call = { url: string; init: RequestInit };

function stub(statuses: number[] = []) {
  const calls: Call[] = [];
  const fetchStub = (async (input: string | URL | Request, init: RequestInit = {}) => {
    calls.push({ url: String(input), init });
    return new Response(null, { status: statuses[calls.length - 1] ?? 202 });
  }) as typeof fetch;
  return { fetch: fetchStub, calls };
}

function expired(...tags: string[]): Map<string, TagRecord> {
  return new Map(tags.map((tag) => [tag, { expired: raisedAt }]));
}

function purger(s: ReturnType<typeof stub>) {
  return newCloudflarePurge({
    url,
    isrPrefix,
    secret,
    fetch: s.fetch,
    sleep: async () => {},
    random: () => 1,
  });
}

function bodiesOf(s: ReturnType<typeof stub>): unknown[] {
  return s.calls.map((call) => JSON.parse(String(call.init.body)));
}

test("an expired tag is purged at the worker under its release's ISR prefix, signed with the deploy's secret", async () => {
  const s = stub();

  await purger(s)(expired("posts", "_N_T_/blog/a"));

  expect(s.calls).toHaveLength(1);
  const call = s.calls[0] as Call;
  expect(call.url).toBe(url);
  expect(call.init.method).toBe("POST");
  const body = String(call.init.body);
  expect(JSON.parse(body)).toEqual({
    isrPrefix,
    mode: "purge",
    tags: [
      { tag: "posts", raisedAt },
      { tag: "_N_T_/blog/a", raisedAt },
    ],
  });
  expect(new Headers(call.init.headers).get(purgeSignatureHeader)).toBe(
    createHmac("sha256", secret).update(body).digest("hex"),
  );
});

test("a tag revalidated stale-while-revalidate is invalidated, raised when it went stale", async () => {
  const s = stub();

  await purger(s)(new Map([["posts", { stale: raisedAt, expired: raisedAt + 31_536_000_000 }]]));

  expect(bodiesOf(s)).toEqual([
    { isrPrefix, mode: "invalidate", tags: [{ tag: "posts", raisedAt }] },
  ]);
});

test("expired and stale tags revalidated together go as a purge and an invalidate", async () => {
  const s = stub();

  await purger(s)(
    new Map<string, TagRecord>([
      ["cart", { expired: raisedAt }],
      ["posts", { stale: raisedAt + 5, expired: raisedAt + 60_000 }],
    ]),
  );

  expect(bodiesOf(s)).toEqual([
    { isrPrefix, mode: "purge", tags: [{ tag: "cart", raisedAt }] },
    { isrPrefix, mode: "invalidate", tags: [{ tag: "posts", raisedAt: raisedAt + 5 }] },
  ]);
});

test("a throttled worker is asked again until it takes the purge", async () => {
  const s = stub([429, 503]);

  await purger(s)(expired("posts"));

  expect(s.calls).toHaveLength(3);
});

test("a worker that refuses the purge fails the revalidation that asked for it", async () => {
  const s = stub([401]);

  await expect(purger(s)(expired("posts"))).rejects.toThrow("401");
});

test("a purge for no tags sends nothing", async () => {
  const s = stub();

  await purger(s)(expired());

  expect(s.calls).toEqual([]);
});

test("a published tag is purged once it has been published, and not before", async () => {
  const s = stub();
  const order: string[] = [];
  const publish = async () => void order.push("published");
  const purge = async (tags: ReadonlyMap<string, TagRecord>) => {
    order.push(`purged ${[...tags.keys()].join(",")}`);
    await purger(s)(tags);
  };

  await withCdnPurge(publish, purge)("posts", { expired: 1 });

  expect(order).toEqual(["published", "purged posts"]);
  expect(s.calls).toHaveLength(1);
});

test("revalidateTag with the max profile reaches the worker as an invalidate", async () => {
  const s = stub();
  const publish = async () => {};

  await withCdnPurge(publish, purger(s))("posts", { stale: raisedAt, expired: raisedAt + 1_000 });

  expect(bodiesOf(s)).toEqual([
    { isrPrefix, mode: "invalidate", tags: [{ tag: "posts", raisedAt }] },
  ]);
});
