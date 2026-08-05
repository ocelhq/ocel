import { expect, it } from "vitest";

import { parseMessage, permittedHosts, type RevalidationMessage } from "../src/message.mjs";
import { trigger, type TriggerDeps } from "../src/trigger.mjs";

const host = "abc123.lambda-url.us-east-1.on.aws";
const credentials = { accessKeyId: "AKIAEXAMPLE", secretAccessKey: "shhh", sessionToken: "session" };

function message(overrides: Record<string, unknown> = {}): RevalidationMessage {
  const parsed = parseMessage(
    JSON.stringify({
      v: 1,
      url: `https://${host}/blog/post`,
      headers: { "x-prerender-revalidate": "token", "x-forwarded-host": "example.com" },
      expect: { header: "x-nextjs-cache", value: "REVALIDATED" },
      isrPrefix: "build-1",
      routePath: "/blog/post",
      lastModified: 1_700_000_000_000,
      enqueuedAt: 1_700_000_000_500,
      ...overrides,
    }),
    permittedHosts(host),
  );
  if (!parsed.ok) throw new Error(`test message rejected: ${parsed.reason}`);
  return parsed.message;
}

function responding(response: Response): { deps: TriggerDeps; requests: Request[] } {
  const requests: Request[] = [];
  const deps: TriggerDeps = {
    credentials,
    fetch: (async (input, init) => {
      requests.push(new Request(input as string | Request, init));
      return response;
    }) as typeof fetch,
  };
  return { deps, requests };
}

function ok(headers: Record<string, string> = {}): Response {
  return new Response(null, { status: 200, headers });
}

it("sends HEAD to the message's url with exactly its headers", async () => {
  const { deps, requests } = responding(ok({ "x-nextjs-cache": "REVALIDATED" }));

  await trigger(deps, message());

  expect(requests).toHaveLength(1);
  const sent = requests[0]!;
  expect(sent.method).toBe("HEAD");
  expect(sent.url).toBe(`https://${host}/blog/post`);
  expect(sent.headers.get("x-prerender-revalidate")).toBe("token");
  expect(sent.headers.get("x-forwarded-host")).toBe("example.com");
});

it("signs as the function's own role against the host's region", async () => {
  const { deps, requests } = responding(ok({ "x-nextjs-cache": "REVALIDATED" }));

  await trigger(deps, message());

  const authorization = requests[0]!.headers.get("authorization") ?? "";
  expect(authorization).toContain("Credential=AKIAEXAMPLE/");
  expect(authorization).toContain("/us-east-1/lambda/aws4_request");
  expect(requests[0]!.headers.get("x-amz-security-token")).toBe("session");
});

it("succeeds when the declared expectation matches", async () => {
  const { deps } = responding(ok({ "x-nextjs-cache": "REVALIDATED" }));

  await expect(trigger(deps, message())).resolves.toEqual({ event: "RevalidateOk" });
});

it("succeeds when no expectation is declared", async () => {
  const { deps } = responding(ok());

  await expect(trigger(deps, message({ expect: null }))).resolves.toEqual({ event: "RevalidateOk" });
});

it("reports an expect miss when the declared header disagrees", async () => {
  const { deps } = responding(ok({ "x-nextjs-cache": "STALE" }));

  await expect(trigger(deps, message())).resolves.toEqual({
    event: "RevalidateExpectMiss",
    expected: "REVALIDATED",
    got: "STALE",
  });
});

it("reports an expect miss when the declared header is absent", async () => {
  const { deps } = responding(ok());

  await expect(trigger(deps, message())).resolves.toEqual({
    event: "RevalidateExpectMiss",
    expected: "REVALIDATED",
    got: null,
  });
});

it("fails on a non-ok response, carrying the status", async () => {
  const { deps } = responding(new Response(null, { status: 429 }));

  await expect(trigger(deps, message())).resolves.toEqual({
    event: "RevalidateFailed",
    reason: "status-not-ok",
    status: 429,
  });
});

it("fails when the fetch throws", async () => {
  const deps: TriggerDeps = {
    credentials,
    fetch: (async () => {
      throw new Error("connect ECONNREFUSED");
    }) as typeof fetch,
  };

  await expect(trigger(deps, message())).resolves.toEqual({
    event: "RevalidateFailed",
    reason: "fetch-failed",
  });
});

it("fails as a timeout when the origin outlasts the budget", async () => {
  const deps: TriggerDeps = {
    credentials,
    timeoutMs: 5,
    fetch: ((_input: string, init: RequestInit) =>
      new Promise((_resolve, reject) => {
        init.signal?.addEventListener("abort", () => reject(new Error("aborted")));
      })) as unknown as typeof fetch,
  };

  await expect(trigger(deps, message())).resolves.toEqual({
    event: "RevalidateFailed",
    reason: "timeout",
  });
});

it("fails without fetching when the pinned host cannot be signed", async () => {
  const { deps, requests } = responding(ok());
  const unsignable = "origin.example.com";

  const parsed = parseMessage(
    JSON.stringify({ ...JSON.parse(JSON.stringify(message())), url: `https://${unsignable}/blog/post` }),
    permittedHosts(unsignable),
  );
  if (!parsed.ok) throw new Error("test message rejected");

  await expect(trigger(deps, parsed.message)).resolves.toEqual({
    event: "RevalidateFailed",
    reason: "unsignable-host",
  });
  expect(requests).toHaveLength(0);
});
