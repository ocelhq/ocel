import { expect, test } from "vitest";
import { newCdnPurge, withCdnPurge } from "../src/next/cdn-purge.mjs";

type Call = { url: string; init: RequestInit };

const metadataPath = "/computeMetadata/v1/instance/service-accounts/default/token";
const urlMap = "projects/p/global/urlMaps/ocel-alb-production-routes";
const release = "r1a2b3c4d";
const secret = "secret-token-value";
const purgeUrl = `https://compute.googleapis.com/compute/v1/${urlMap}/invalidateCache`;

function stub(statuses: number[]) {
  const calls: Call[] = [];
  const purges: Call[] = [];
  let metadataFetches = 0;
  const fetchStub = (async (input: string | URL | Request, init: RequestInit = {}) => {
    const call = { url: String(input), init };
    calls.push(call);
    if (call.url.includes(metadataPath)) {
      metadataFetches++;
      return Response.json({ access_token: secret, expires_in: 3600 });
    }
    purges.push(call);
    return Response.json({}, { status: statuses[purges.length - 1] ?? 200 });
  }) as typeof fetch;
  return { fetch: fetchStub, calls, purges, metadataFetches: () => metadataFetches };
}

const noSleep = async () => {};

function purger(s: ReturnType<typeof stub>, warn: (message: string) => void = () => {}) {
  return newCdnPurge({ urlMap, release, fetch: s.fetch, sleep: noSleep, random: () => 1, warn });
}

function bodyOf(call: Call): { cacheTags: string[] } {
  return JSON.parse(call.init.body as string);
}

test("a revalidated tag is cleared from Cloud CDN under its release", async () => {
  const s = stub([]);

  await purger(s)(["posts"]);

  expect(s.purges).toHaveLength(1);
  const call = s.purges[0] as Call;
  expect(call.url).toBe(purgeUrl);
  expect(call.init.method).toBe("POST");
  expect(bodyOf(call)).toEqual({ cacheTags: ["r1a2b3c4d|posts"] });
  const headers = new Headers(call.init.headers);
  expect(headers.get("Authorization")).toBe(`Bearer ${secret}`);
  expect(headers.get("Content-Type")).toBe("application/json");
});

test("tags revalidated together are cleared in one request", async () => {
  const s = stub([]);
  const purge = purger(s);

  await Promise.all([purge(["a", "b"]), purge(["b", "c"])]);

  expect(s.purges).toHaveLength(1);
  expect(bodyOf(s.purges[0] as Call)).toEqual({
    cacheTags: ["r1a2b3c4d|a", "r1a2b3c4d|b", "r1a2b3c4d|c"],
  });
});

test("eleven tags are cleared in two requests of at most ten", async () => {
  const s = stub([]);
  const tags = Array.from({ length: 11 }, (_, i) => `t${i}`);

  await purger(s)(tags);

  expect(s.purges.map((call) => bodyOf(call).cacheTags.length)).toEqual([10, 1]);
});

test("a throttled purge is retried until Cloud CDN takes it", async () => {
  const s = stub([429, 503, 200]);

  await purger(s)(["posts"]);

  expect(s.purges).toHaveLength(3);
});

test("a purge Cloud CDN keeps refusing rejects after four attempts naming the release, not the token", async () => {
  const s = stub([503, 503, 503, 503]);

  const error = await purger(s)(["posts"]).then(
    () => undefined,
    (e: Error) => e,
  );

  expect(s.purges).toHaveLength(4);
  expect(error?.message).toContain(release);
  expect(error?.message).not.toContain(secret);
});

test("a purge refused for lack of permission rejects at once and names the role", async () => {
  const s = stub([403]);

  const error = await purger(s)(["posts"]).then(
    () => undefined,
    (e: Error) => e,
  );

  expect(s.purges).toHaveLength(1);
  expect(error?.message).toContain("cache purge role");
  expect(error?.message).not.toContain(secret);
});

test("an expired token is replaced once", async () => {
  const s = stub([401, 200]);

  await purger(s)(["posts"]);

  expect(s.metadataFetches()).toBe(2);
  expect(s.purges).toHaveLength(2);
});

test("a tag too long to stamp is never sent, and says so", async () => {
  const s = stub([]);
  const warnings: string[] = [];
  const long = "x".repeat(115);

  await purger(s, (message) => warnings.push(message))([long]);

  expect(s.purges).toHaveLength(0);
  expect(warnings).toHaveLength(1);
  expect(warnings[0]).toContain(long);
});

test("a revalidation resolves only after Cloud CDN took its tags", async () => {
  const s = stub([]);
  const order: string[] = [];
  const publish = async () => {
    order.push("publish");
  };
  const fetchSpy = (async (input: string | URL | Request, init?: RequestInit) => {
    if (!String(input).includes(metadataPath)) order.push("post");
    return s.fetch(input, init);
  }) as typeof fetch;
  const purge = newCdnPurge({ urlMap, release, fetch: fetchSpy, sleep: noSleep });

  await withCdnPurge(publish, purge)("posts", { expired: 1 });
  order.push("resolved");

  expect(order).toEqual(["publish", "post", "resolved"]);
});

test("a revalidation whose tag write fails clears nothing and rejects with that failure", async () => {
  const s = stub([]);
  const publish = async () => {
    throw new Error("write failed");
  };

  await expect(withCdnPurge(publish, purger(s))("posts", { expired: 1 })).rejects.toThrow(
    "write failed",
  );
  expect(s.purges).toHaveLength(0);
});
