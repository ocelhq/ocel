import { afterEach, beforeEach, expect, it, vi } from "vitest";

import { handler } from "../src/index.mjs";

const host = "abc123.lambda-url.us-east-1.on.aws";

const record = {
  messageId: "m-1",
  body: JSON.stringify({
    v: 1,
    url: `https://${host}/blog/post`,
    headers: { "x-prerender-revalidate": "token" },
    expect: null,
    isrPrefix: "build-1",
    routePath: "/blog/post",
    lastModified: 1_700_000_000_000,
    enqueuedAt: 1_700_000_000_500,
  }),
  attributes: { MessageGroupId: "build-1:/blog/post" },
};

let fetched: string[];

beforeEach(() => {
  fetched = [];
  vi.spyOn(console, "log").mockImplementation(() => {});
  vi.stubGlobal("fetch", async (input: string | Request) => {
    fetched.push(typeof input === "string" ? input : input.url);
    return new Response(null, { status: 200 });
  });
  vi.stubEnv("AWS_ACCESS_KEY_ID", "AKIAEXAMPLE");
  vi.stubEnv("AWS_SECRET_ACCESS_KEY", "shhh");
  vi.stubEnv("AWS_SESSION_TOKEN", "session");
});

afterEach(() => {
  vi.unstubAllEnvs();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

it("triggers a record whose host the environment permits", async () => {
  vi.stubEnv("OCEL_REVALIDATE_ALLOWED_HOSTS", host);

  await expect(handler({ Records: [record] })).resolves.toEqual({ batchItemFailures: [] });
  expect(fetched).toEqual([`https://${host}/blog/post`]);
});

it("permits nothing, and fetches nothing, when no host is configured", async () => {
  vi.stubEnv("OCEL_REVALIDATE_ALLOWED_HOSTS", "");

  await expect(handler({ Records: [record] })).resolves.toEqual({
    batchItemFailures: [{ itemIdentifier: "m-1" }],
  });
  expect(fetched).toEqual([]);
});

it("reads the environment per invocation, so rotated credentials are the ones it signs with", async () => {
  vi.stubEnv("OCEL_REVALIDATE_ALLOWED_HOSTS", host);
  const signatures: string[] = [];
  vi.stubGlobal("fetch", async (_input: string, init: RequestInit) => {
    signatures.push(new Headers(init.headers).get("authorization") ?? "");
    return new Response(null, { status: 200 });
  });

  await handler({ Records: [record] });
  vi.stubEnv("AWS_ACCESS_KEY_ID", "AKIAROTATED");
  await handler({ Records: [record] });

  expect(signatures[0]).toContain("Credential=AKIAEXAMPLE/");
  expect(signatures[1]).toContain("Credential=AKIAROTATED/");
});
