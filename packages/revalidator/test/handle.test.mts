import { afterEach, beforeEach, expect, it, vi } from "vitest";

import { handle, type HandlerDeps, type SqsRecord } from "../src/handle.mjs";
import { body, bucket, credentials, host, originDocument, recordUrl, region } from "./fixture.mjs";

const bypassToken = "s3cr3t-preview-mode-id";

function record(messageId: string, group: string, overrides: Record<string, unknown> = {}): SqsRecord {
  return { messageId, body: body(overrides), attributes: { MessageGroupId: group } };
}

// The substrate: S3 answering the deploy record, and the origin answering by
// the path each trigger asks for — a Response, or an Error to throw.
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

// Every test runs with the log captured: these lines are the audit trail the
// no-log-the-token rule is about, and a test that let them reach the terminal
// would be printing a bypass token into CI output.
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
  // The stopped group's later record is never triggered; the other group runs on.
  expect(requested).toEqual(["/a/1", "/b/1", "/b/2"]);
});

// A record with no group attribute belongs to no group but its own. Keying it
// by its message id and keying it by a shared constant are both green against
// every other test here, and they mean opposite things: the second lets one
// failure suppress every later ungrouped record in the batch.
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

// A record reported without ever being tried is still a record on its way to
// the DLQ. CloudWatch cannot tell "tried and failed" from "never tried" unless
// the skip says so itself.
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

// Nothing the message says can name a host, so the failure the old allowlist
// caught now cannot arise: a route the deploy did not record simply has no
// origin to trigger.
it("fails a record whose route the deploy never recorded, without triggering", async () => {
  const { deps, requested } = substrate();

  const response = await handle(deps, { Records: [record("m-1", "group-a", { routeId: "/not-a-route" })] });

  expect(failures(response)).toEqual(["m-1"]);
  expect(requested).toEqual([]);
  expect(JSON.parse(lines[0]!).reason).toBe("origin-unusable");
});

it("keeps a record whose expectation missed out of the failures, and logs it", async () => {
  const { deps } = substrate({ "/blog/post": new Response(null, { status: 200 }) });

  const response = await handle(deps, { Records: [record("m-1", "group-a")] });

  expect(failures(response)).toEqual([]);
  expect(events(lines)).toEqual(["RevalidateExpectMiss"]);
});

it("fails the item, never the batch, when signing itself throws", async () => {
  const { deps } = substrate();
  // A role whose credentials arrived half-formed: the signer throws where
  // nothing expects it to, and a thrown handler would fail every group in the
  // batch rather than these records.
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
