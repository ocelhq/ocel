import { afterEach, beforeEach, expect, it, vi } from "vitest";

import { handle, type HandlerDeps, type SqsRecord } from "../src/handle.mjs";
import { body, bucket, credentials, host, originDocument, recordUrl, region } from "./fixture.mjs";

const bypassToken = "s3cr3t-preview-mode-id";

function record(messageId: string, group: string, overrides: Record<string, unknown> = {}): SqsRecord {
  return { messageId, body: body(overrides), attributes: { MessageGroupId: group } };
}

function substrate(answers: Record<string, Response | Error> = {}): {
  deps: HandlerDeps;
  requested: string[];
} {
  const requested: string[] = [];
  const deps: HandlerDeps = {
    credentials,
    bucket,
    region,
    origins: new Map(),
    fetch: (async (input: string | Request) => {
      const url = typeof input === "string" ? input : input.url;
      if (url === recordUrl) return new Response(originDocument(), { status: 200 });
      const path = new URL(url).pathname;
      requested.push(path);
      const answer = answers[path] ?? new Response(null, { status: 200 });
      if (answer instanceof Error) throw answer;
      return answer;
    }) as typeof fetch,
  };
  return { deps, requested };
}

const revalidated = new Response(null, { status: 200, headers: { "x-nextjs-cache": "REVALIDATED" } });

function failures(response: { batchItemFailures: { itemIdentifier: string }[] }): string[] {
  return response.batchItemFailures.map(({ itemIdentifier }) => itemIdentifier);
}

function events(lines: string[]): string[] {
  return lines.map((line) => JSON.parse(line).event as string);
}

let lines: string[] = [];

beforeEach(() => {
  lines = [];
  vi.spyOn(console, "log").mockImplementation((line: string) => void lines.push(line));
});

afterEach(() => vi.restoreAllMocks());

it("answers an empty batch without reaching the origin", async () => {
  const { deps, requested } = substrate();

  await expect(handle(deps, { Records: [] })).resolves.toEqual({ batchItemFailures: [] });
  expect(requested).toEqual([]);
});

it("stops a group at its first failure and reports the rest of that group, unprocessed", async () => {
  const { deps, requested } = substrate({
    "/a/1": new Response(null, { status: 500 }),
    "/a/2": revalidated,
    "/b/1": revalidated,
    "/b/2": revalidated,
  });

  const response = await handle(deps, {
    Records: [
      record("a-1", "group-a", { routePath: "/a/1" }),
      record("b-1", "group-b", { routePath: "/b/1" }),
      record("a-2", "group-a", { routePath: "/a/2" }),
      record("b-2", "group-b", { routePath: "/b/2" }),
    ],
  });

  expect(failures(response)).toEqual(["a-1", "a-2"]);
  expect(requested).toEqual(["/a/1", "/b/1", "/b/2"]);
});

it("keeps a record carrying no group from stopping the records after it", async () => {
  const { deps, requested } = substrate({ "/a": new Response(null, { status: 500 }), "/b": revalidated });

  const response = await handle(deps, {
    Records: [
      { messageId: "m-1", body: body({ routePath: "/a" }) },
      { messageId: "m-2", body: body({ routePath: "/b" }) },
    ],
  });

  expect(failures(response)).toEqual(["m-1"]);
  expect(requested).toEqual(["/a", "/b"]);
});

it("treats an empty group id the same as a missing one", async () => {
  const { deps, requested } = substrate({ "/a": new Response(null, { status: 500 }), "/b": revalidated });

  const response = await handle(deps, {
    Records: [
      { messageId: "m-1", body: body({ routePath: "/a" }), attributes: { MessageGroupId: "" } },
      { messageId: "m-2", body: body({ routePath: "/b" }), attributes: { MessageGroupId: "" } },
    ],
  });

  expect(failures(response)).toEqual(["m-1"]);
  expect(requested).toEqual(["/a", "/b"]);
});

it("logs the record it skipped for a stopped group, as well as reporting it", async () => {
  const { deps } = substrate({ "/a/1": new Response(null, { status: 500 }) });

  await handle(deps, {
    Records: [record("a-1", "group-a", { routePath: "/a/1" }), record("a-2", "group-a", { routePath: "/a/2" })],
  });

  expect(events(lines)).toEqual(["RevalidateFailed", "RevalidateSkipped"]);
  expect(JSON.parse(lines[1]!)).toEqual({
    event: "RevalidateSkipped",
    reason: "group-stopped",
    messageId: "a-2",
  });
});

it("rejects an unknown message version as an item failure", async () => {
  const { deps, requested } = substrate();

  const response = await handle(deps, { Records: [record("m-1", "group-a", { v: 2 })] });

  expect(failures(response)).toEqual(["m-1"]);
  expect(requested).toEqual([]);
});

it("fails a record whose route the deploy never recorded, without triggering", async () => {
  const { deps, requested } = substrate();

  const response = await handle(deps, { Records: [record("m-1", "group-a", { routeId: "/not-a-route" })] });

  expect(failures(response)).toEqual(["m-1"]);
  expect(requested).toEqual([]);
  expect(JSON.parse(lines[0]!).reason).toBe("origin-unusable");
});

it("reads nothing at all for an isrPrefix that names a key the edge could have planted", async () => {
  const fetched: string[] = [];
  const { deps } = substrate();
  const watched: HandlerDeps = {
    ...deps,
    fetch: (async (input: string | Request) => {
      fetched.push(typeof input === "string" ? input : input.url);
      return new Response(null, { status: 200 });
    }) as typeof fetch,
  };

  const response = await handle(watched, {
    Records: [
      record("m-1", "g-1", { isrPrefix: "prod/proj/web/BID/fetch-cache/planted.cache.json#" }),
      record("m-2", "g-2", { isrPrefix: "prod/proj/web/BID/fetch-cache/planted.cache.json?" }),
      record("m-3", "g-3", { isrPrefix: "prod/proj/web/BID/../../../other-app/BID2" }),
    ],
  });

  expect(failures(response)).toEqual(["m-1", "m-2", "m-3"]);
  expect(fetched).toEqual([]);
  expect(lines.map((line) => JSON.parse(line).reason as string)).toEqual([
    "malformed",
    "malformed",
    "malformed",
  ]);
});

it("keeps a record whose expectation missed out of the failures, and logs it", async () => {
  const { deps } = substrate({ "/blog/post": new Response(null, { status: 200 }) });

  const response = await handle(deps, { Records: [record("m-1", "group-a")] });

  expect(failures(response)).toEqual([]);
  expect(events(lines)).toEqual(["RevalidateExpectMiss"]);
});

it("fails the item, never the batch, when signing itself throws", async () => {
  const { deps } = substrate();
  const broken: HandlerDeps = {
    ...deps,
    origins: new Map(),
    credentials: { accessKeyId: "AKIAEXAMPLE" } as HandlerDeps["credentials"],
  };

  const response = await handle(broken, {
    Records: [record("m-1", "group-a"), record("m-2", "group-b")],
  });

  expect(failures(response)).toEqual(["m-1", "m-2"]);
  expect(events(lines)).toEqual(["RevalidateFailed", "RevalidateFailed"]);
});

it("never emits the bypass token, on the success path or any failure path", async () => {
  const { deps } = substrate({
    "/ok": revalidated,
    "/miss": new Response(null, { status: 200 }),
    "/down": new Response(null, { status: 503 }),
    "/throws": new Error(`fetch failed for header x-prerender-revalidate: ${bypassToken}`),
  });

  await handle(deps, {
    Records: [
      record("ok", "g-ok", { routePath: "/ok" }),
      record("miss", "g-miss", { routePath: "/miss" }),
      record("down", "g-down", { routePath: "/down" }),
      record("throws", "g-throws", { routePath: "/throws" }),
      {
        messageId: "junk",
        body: `{"v":1,"headers":{"x-prerender-revalidate":"${bypassToken}"`,
        attributes: { MessageGroupId: "g-junk" },
      },
    ],
  });

  expect(lines).toHaveLength(5);
  expect(lines.join("\n")).not.toContain(bypassToken);
  expect(lines.join("\n")).not.toContain(host);
});
