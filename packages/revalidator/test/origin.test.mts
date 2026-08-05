import { expect, it, vi } from "vitest";

import { originTimeoutMs } from "../src/limits.mjs";
import { parseMessage, type RevalidationMessage } from "../src/message.mjs";
import { resolve, type OriginDeps } from "../src/origin.mjs";
import { body, bucket, credentials, host, isrPrefix, originDocument, recordUrl, region } from "./fixture.mjs";

function message(overrides: Record<string, unknown> = {}): RevalidationMessage {
  const parsed = parseMessage(body(overrides));
  if (!parsed.ok) throw new Error(`test message rejected: ${parsed.reason}`);
  return parsed.message;
}

// The substrate's S3, answering the deploy record with whatever this test wants
// it to say — including nothing.
function substrate(answer: Response | Error | (() => Response)): {
  deps: OriginDeps;
  requests: Request[];
} {
  const requests: Request[] = [];
  const deps: OriginDeps = {
    credentials,
    bucket,
    region,
    origins: new Map(),
    fetch: (async (input, init) => {
      requests.push(new Request(input as string | Request, init));
      if (answer instanceof Error) throw answer;
      return typeof answer === "function" ? answer() : answer;
    }) as typeof fetch,
  };
  return { deps, requests };
}

function record(document: string = originDocument()): () => Response {
  return () => new Response(document, { status: 200 });
}

it("reads the deploy's own record, under the isrPrefix, signed as the function's role", async () => {
  const { deps, requests } = substrate(record());

  const resolution = await resolve(deps, message());

  expect(resolution).toEqual({ ok: true, target: { url: `https://${host}/blog/post`, region: "us-east-1" } });
  expect(requests[0]!.url).toBe(recordUrl);
  expect(requests[0]!.headers.get("authorization")).toContain(`/${region}/s3/aws4_request`);
});

it("composes the trigger from the recorded origin and the message's route path", async () => {
  const { deps } = substrate(record());

  const resolution = await resolve(deps, message({ routePath: "/deep/route" }));

  expect(resolution.ok && resolution.target.url).toBe(`https://${host}/deep/route`);
});

// The whole point of resolving rather than validating: a route path that tries
// to be a host cannot become one, because the composed origin is compared back
// to the recorded one.
it("refuses a route path that would leave the recorded origin", async () => {
  const { deps } = substrate(record());

  await expect(resolve(deps, message({ routePath: "//attacker.example.com/x" }))).resolves.toEqual({
    ok: false,
    reason: "origin-unusable",
  });
});

it("refuses a route id the deploy never recorded", async () => {
  const { deps } = substrate(record());

  await expect(resolve(deps, message({ routeId: "/not-a-route" }))).resolves.toEqual({
    ok: false,
    reason: "origin-unusable",
  });
});

// The host shape is not what makes the token safe — the record is — but a
// record that somehow said something else must not be signed for anyway, and
// "a label somewhere equal to lambda-url" is not a Function URL check: it
// admits `attacker.lambda-url.us-east-1.evil.example`, whose region it would
// then read as `us-east-1` and sign against.
it.each([
  ["a host of another shape entirely", "https://attacker.example.com/"],
  ["a suffix that only looks like one", "https://attacker.lambda-url.us-east-1.evil.example/"],
  ["a deeper name under the real suffix", "https://a.b.lambda-url.us-east-1.on.aws/"],
  ["a suffix that merely ends the same way", "https://abc123.lambda-url.us-east-1.not-on.aws/"],
])("refuses a recorded origin that is not a Function URL: %s", async (_name, origin) => {
  const { deps } = substrate(record(originDocument({ "/": origin })));

  await expect(resolve(deps, message())).resolves.toEqual({ ok: false, reason: "origin-unusable" });
});

it("refuses a recorded origin reached over http", async () => {
  const { deps } = substrate(record(originDocument({ "/": `http://${host}/` })));

  await expect(resolve(deps, message())).resolves.toEqual({ ok: false, reason: "origin-unusable" });
});

it("refuses a record of an unknown version", async () => {
  const { deps } = substrate(record(JSON.stringify({ v: 2, functionUrls: { "/": `https://${host}/` } })));

  await expect(resolve(deps, message())).resolves.toEqual({ ok: false, reason: "origin-unusable" });
});

it("refuses a record that is not JSON", async () => {
  const { deps } = substrate(record("{"));

  await expect(resolve(deps, message())).resolves.toEqual({ ok: false, reason: "origin-unusable" });
});

// A deploy older than the record answers 404 forever; an S3 that is merely
// unwell answers 503. Only one of them is worth redelivering for, and the
// reason code is what says which.
it("calls a missing record unusable and a failing read unavailable", async () => {
  const missing = substrate(() => new Response(null, { status: 404 }));
  const failing = substrate(() => new Response(null, { status: 503 }));

  await expect(resolve(missing.deps, message())).resolves.toEqual({ ok: false, reason: "origin-unusable" });
  await expect(resolve(failing.deps, message())).resolves.toEqual({ ok: false, reason: "origin-unavailable" });
});

it("reports an unreachable record as unavailable rather than throwing", async () => {
  const { deps } = substrate(new Error("connect ECONNREFUSED"));

  await expect(resolve(deps, message())).resolves.toEqual({ ok: false, reason: "origin-unavailable" });
});

// The default budget is the one production runs on — no caller passes
// originTimeoutMs — so asserting it means catching the number the signal was
// built from AND that the signal built from it is the one the read waits on.
it("reads on the documented budget when no caller overrides it", async () => {
  const { deps } = substrate(record());
  const timeout = vi.spyOn(AbortSignal, "timeout");
  const signals: (AbortSignal | null | undefined)[] = [];
  deps.fetch = (async (_input: string, init: RequestInit) => {
    signals.push(init.signal);
    return new Response(originDocument(), { status: 200 });
  }) as typeof fetch;

  await resolve(deps, message());

  expect(timeout).toHaveBeenCalledWith(originTimeoutMs);
  expect(signals[0]).toBe(timeout.mock.results[0]!.value);
  timeout.mockRestore();
});

it("resolves nothing, and reads nothing, when no asset bucket is configured", async () => {
  const { deps, requests } = substrate(record());
  deps.bucket = undefined;

  await expect(resolve(deps, message())).resolves.toEqual({ ok: false, reason: "origin-unconfigured" });
  expect(requests).toEqual([]);
});

it("reads one record per isrPrefix, however many routes of it a batch carries", async () => {
  const { deps, requests } = substrate(record());

  await resolve(deps, message({ routePath: "/a" }));
  await resolve(deps, message({ routePath: "/b" }));
  await resolve(deps, message({ isrPrefix: "prod/proj/web/OTHER" }));

  expect(requests.map((request) => request.url)).toEqual([
    recordUrl,
    recordUrl.replace(isrPrefix, "prod/proj/web/OTHER"),
  ]);
});
