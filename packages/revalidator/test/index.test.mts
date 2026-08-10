import { afterEach, beforeEach, expect, it, vi } from "vitest";

import { handler } from "../src/index.mjs";
import { body, bucket, host, originDocument, recordUrl, region } from "./fixture.mjs";

const record = { messageId: "m-1", body: body({ expect: null }), attributes: { MessageGroupId: "g-1" } };

let fetched: string[];

beforeEach(() => {
  fetched = [];
  vi.spyOn(console, "log").mockImplementation(() => {});
  vi.stubGlobal("fetch", async (input: string | Request) => {
    const url = typeof input === "string" ? input : input.url;
    fetched.push(url);
    if (url === recordUrl) return new Response(originDocument(), { status: 200 });
    return new Response(null, { status: 200 });
  });
  vi.stubEnv("AWS_ACCESS_KEY_ID", "AKIAEXAMPLE");
  vi.stubEnv("AWS_SECRET_ACCESS_KEY", "shhh");
  vi.stubEnv("AWS_SESSION_TOKEN", "session");
  vi.stubEnv("AWS_REGION", region);
});

afterEach(() => {
  vi.unstubAllEnvs();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

it("resolves the origin from the deploy's own record and triggers it", async () => {
  vi.stubEnv("OCEL_ASSET_BUCKET", bucket);

  await expect(handler({ Records: [record] })).resolves.toEqual({ batchItemFailures: [] });
  expect(fetched).toEqual([recordUrl, `https://${host}/blog/post`]);
});

it("triggers nothing when it was never told where the deploy records live", async () => {
  vi.stubEnv("OCEL_ASSET_BUCKET", "");

  await expect(handler({ Records: [record] })).resolves.toEqual({
    batchItemFailures: [{ itemIdentifier: "m-1" }],
  });
  expect(fetched).toEqual([]);
});

it("reads the environment per invocation, so rotated credentials are the ones it signs with", async () => {
  vi.stubEnv("OCEL_ASSET_BUCKET", bucket);
  const signatures: string[] = [];
  vi.stubGlobal("fetch", async (input: string, init: RequestInit) => {
    if (input === recordUrl) return new Response(originDocument(), { status: 200 });
    signatures.push(new Headers(init.headers).get("authorization") ?? "");
    return new Response(null, { status: 200 });
  });

  await handler({ Records: [record] });
  vi.stubEnv("AWS_ACCESS_KEY_ID", "AKIAROTATED");
  await handler({ Records: [record] });

  expect(signatures[0]).toContain("Credential=AKIAEXAMPLE/");
  expect(signatures[1]).toContain("Credential=AKIAROTATED/");
});

it("re-reads the deploy record on the next invocation rather than remembering it", async () => {
  vi.stubEnv("OCEL_ASSET_BUCKET", bucket);

  await handler({ Records: [record] });
  await handler({ Records: [record] });

  expect(fetched.filter((url) => url === recordUrl)).toHaveLength(2);
});
