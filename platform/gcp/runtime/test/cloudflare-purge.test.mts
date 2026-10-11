import { createHmac } from "node:crypto";

import { purgeSignatureHeader } from "@platform/edge-contract/purge";
import { expect, test } from "vitest";

import { withCdnPurge } from "../src/next/cdn-purge.mjs";
import { newCloudflarePurge } from "../src/next/cloudflare-purge.mjs";

const url = "https://shop-prod.acct.workers.dev/.ocel/purge";
const release = "r1a2b3c4d";
const secret = "write-secret";

type Call = { url: string; init: RequestInit };

function stub(statuses: number[] = []) {
  const calls: Call[] = [];
  const fetchStub = (async (input: string | URL | Request, init: RequestInit = {}) => {
    calls.push({ url: String(input), init });
    return new Response(null, { status: statuses[calls.length - 1] ?? 202 });
  }) as typeof fetch;
  return { fetch: fetchStub, calls };
}

function purger(s: ReturnType<typeof stub>) {
  return newCloudflarePurge({
    url,
    release,
    secret,
    fetch: s.fetch,
    sleep: async () => {},
    random: () => 1,
    now: () => 1_700_000_000_000,
  });
}

test("a revalidated tag is purged at the worker under its release, signed with the deploy's secret", async () => {
  const s = stub();

  await purger(s)(["posts", "_N_T_/blog/a"]);

  expect(s.calls).toHaveLength(1);
  const call = s.calls[0] as Call;
  expect(call.url).toBe(url);
  expect(call.init.method).toBe("POST");
  const body = String(call.init.body);
  expect(JSON.parse(body)).toEqual({
    release,
    mode: "purge",
    tags: [
      { tag: "posts", raisedAt: 1_700_000_000_000 },
      { tag: "_N_T_/blog/a", raisedAt: 1_700_000_000_000 },
    ],
  });
  expect(new Headers(call.init.headers).get(purgeSignatureHeader)).toBe(
    createHmac("sha256", secret).update(body).digest("hex"),
  );
});

test("a throttled worker is asked again until it takes the purge", async () => {
  const s = stub([429, 503]);

  await purger(s)(["posts"]);

  expect(s.calls).toHaveLength(3);
});

test("a worker that refuses the purge fails the revalidation that asked for it", async () => {
  const s = stub([401]);

  await expect(purger(s)(["posts"])).rejects.toThrow("401");
});

test("a purge for no tags sends nothing", async () => {
  const s = stub();

  await purger(s)([]);

  expect(s.calls).toEqual([]);
});

test("a published tag is purged once it has been published, and not before", async () => {
  const s = stub();
  const order: string[] = [];
  const publish = async () => void order.push("published");
  const purge = async (tags: readonly string[]) => {
    order.push(`purged ${tags.join(",")}`);
    await purger(s)(tags);
  };

  await withCdnPurge(publish, purge)("posts", { expired: 1 });

  expect(order).toEqual(["published", "purged posts"]);
  expect(s.calls).toHaveLength(1);
});
