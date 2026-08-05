import { afterEach, beforeEach, expect, it, vi } from "vitest";

import { handle, type HandlerDeps, type SqsRecord } from "../src/handle.mjs";
import { permittedHosts } from "../src/message.mjs";

const host = "abc123.lambda-url.us-east-1.on.aws";
const bypassToken = "s3cr3t-preview-mode-id";

const credentials = { accessKeyId: "AKIAEXAMPLE", secretAccessKey: "shhh" };

function body(overrides: Record<string, unknown> = {}): string {
  return JSON.stringify({
    v: 1,
    url: `https://${host}/blog/post`,
    headers: { "x-prerender-revalidate": bypassToken },
    expect: { header: "x-nextjs-cache", value: "REVALIDATED" },
    isrPrefix: "build-1",
    routePath: "/blog/post",
    lastModified: 1_700_000_000_000,
    enqueuedAt: 1_700_000_000_500,
    ...overrides,
  });
}

function record(messageId: string, group: string, overrides: Record<string, unknown> = {}): SqsRecord {
  return { messageId, body: body(overrides), attributes: { MessageGroupId: group } };
}

// The origin, keyed by the path each record asks for: a Response to answer
// with, or an Error to throw.
function origin(answers: Record<string, Response | Error>): {
  deps: HandlerDeps;
  requested: string[];
} {
  const requested: string[] = [];
  const deps: HandlerDeps = {
    credentials,
    hosts: permittedHosts(host),
    fetch: (async (input: string | Request) => {
      const url = typeof input === "string" ? input : input.url;
      requested.push(new URL(url).pathname);
      const answer = answers[new URL(url).pathname] ?? new Response(null, { status: 200 });
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
  const { deps, requested } = origin({});

  await expect(handle(deps, { Records: [] })).resolves.toEqual({ batchItemFailures: [] });
  expect(requested).toEqual([]);
});

it("stops a group at its first failure and reports the rest of that group, unprocessed", async () => {
  const { deps, requested } = origin({
    "/a/1": new Response(null, { status: 500 }),
    "/a/2": revalidated,
    "/b/1": revalidated,
    "/b/2": revalidated,
  });

  const response = await handle(deps, {
    Records: [
      record("a-1", "group-a", { url: `https://${host}/a/1`, routePath: "/a/1" }),
      record("b-1", "group-b", { url: `https://${host}/b/1`, routePath: "/b/1" }),
      record("a-2", "group-a", { url: `https://${host}/a/2`, routePath: "/a/2" }),
      record("b-2", "group-b", { url: `https://${host}/b/2`, routePath: "/b/2" }),
    ],
  });

  expect(failures(response)).toEqual(["a-1", "a-2"]);
  // The stopped group's later record is never triggered; the other group runs on.
  expect(requested).toEqual(["/a/1", "/b/1", "/b/2"]);
});

it("rejects a host outside the permitted set without fetching", async () => {
  const { deps, requested } = origin({});

  const response = await handle(deps, {
    Records: [record("m-1", "group-a", { url: "https://attacker.lambda-url.us-east-1.on.aws/blog/post" })],
  });

  expect(failures(response)).toEqual(["m-1"]);
  expect(requested).toEqual([]);
});

it("rejects an unknown message version as an item failure", async () => {
  const { deps, requested } = origin({});

  const response = await handle(deps, { Records: [record("m-1", "group-a", { v: 2 })] });

  expect(failures(response)).toEqual(["m-1"]);
  expect(requested).toEqual([]);
});

it("keeps a record whose expectation missed out of the failures, and logs it", async () => {
  const { deps } = origin({ "/blog/post": new Response(null, { status: 200 }) });

  const response = await handle(deps, { Records: [record("m-1", "group-a")] });

  expect(failures(response)).toEqual([]);
  expect(lines.map((line) => JSON.parse(line).event)).toEqual(["RevalidateExpectMiss"]);
});

it("fails the item, never the batch, when signing itself throws", async () => {
  const { deps } = origin({});
  // A role whose credentials arrived half-formed: the signer throws where
  // nothing expects it to, and a thrown handler would fail every group in the
  // batch rather than this one record.
  const broken: HandlerDeps = {
    ...deps,
    credentials: { accessKeyId: "AKIAEXAMPLE" } as HandlerDeps["credentials"],
  };

  const response = await handle(broken, {
    Records: [record("m-1", "group-a"), record("m-2", "group-b")],
  });

  expect(failures(response)).toEqual(["m-1", "m-2"]);
});

it("never emits the bypass token, on the success path or any failure path", async () => {
  const { deps } = origin({
    "/ok": revalidated,
    "/miss": new Response(null, { status: 200 }),
    "/down": new Response(null, { status: 503 }),
    "/throws": new Error(`fetch failed for header x-prerender-revalidate: ${bypassToken}`),
  });

  await handle(deps, {
    Records: [
      record("ok", "g-ok", { url: `https://${host}/ok`, routePath: "/ok" }),
      record("miss", "g-miss", { url: `https://${host}/miss`, routePath: "/miss" }),
      record("down", "g-down", { url: `https://${host}/down`, routePath: "/down" }),
      record("throws", "g-throws", { url: `https://${host}/throws`, routePath: "/throws" }),
      { messageId: "junk", body: `{"v":1,"headers":{"x-prerender-revalidate":"${bypassToken}"`, attributes: { MessageGroupId: "g-junk" } },
    ],
  });

  expect(lines).toHaveLength(5);
  expect(lines.join("\n")).not.toContain(bypassToken);
});
