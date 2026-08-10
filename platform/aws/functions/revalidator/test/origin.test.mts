import { expect, it, vi } from "vitest";

import { originTimeoutMs } from "../src/limits.mjs";
import { parseMessage, type RevalidationMessage } from "@platform/edge-contract/revalidation";
import { resolve, type OriginDeps, type Target } from "../src/origin.mjs";
import { body, bucket, credentials, host, isrPrefix, originDocument, recordUrl, region } from "./fixture.mjs";

function message(overrides: Record<string, unknown> = {}): RevalidationMessage {
  const parsed = parseMessage(body(overrides));
  if (!parsed.ok) throw new Error(`test message rejected: ${parsed.reason}`);
  return parsed.message;
}

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

// a comment: `@ts-expect-error` is itself an error when the error it names does
it("cannot be spelled from a literal, or from a copy of a real one", async () => {
  const { deps } = substrate(record());
  const resolution = await resolve(deps, message());
  if (!resolution.ok) throw new Error(resolution.reason);

  // @ts-expect-error a literal carries no resolution
  const fabricated: Target = { url: "https://attacker.example.com/", region: "us-east-1" };
  // @ts-expect-error and copying a real one drops what made it one
  const copied: Target = { ...resolution.target, url: "https://attacker.example.com/" };

  expect([fabricated.region, copied.region]).toEqual(["us-east-1", "us-east-1"]);
});

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
