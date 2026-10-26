import { refuseIncompleteHost } from "@framework/next-runtime/host";
import { afterEach, expect, test, vi } from "vitest";
import { awsDispatchAccess } from "../src/next/dispatch-host.mjs";
import { newAwsFunctionContainerHost } from "../src/next/function-container-host.mjs";

const taskCredentials = async () => ({
  accessKeyId: "ASIATASK",
  secretAccessKey: "task-secret",
  sessionToken: "task-session",
});

afterEach(() => vi.unstubAllGlobals());

test("a container reads its static files out of the edge bucket with the task's credentials", async () => {
  const seen: Request[] = [];
  vi.stubGlobal("fetch", async (input: Request) => {
    seen.push(new Request(input));
    return new Response("chunk", { headers: { etag: '"e1"' } });
  });
  const access = awsDispatchAccess(
    { AWS_REGION: "us-east-1", OCEL_EDGE_BUCKET: "edge" },
    taskCredentials,
  );

  const object = await access.assetBucket?.get("assets/shop/prod/web/r1/_next/static/app.js");

  expect(object?.httpEtag).toBe('"e1"');
  expect(seen[0]?.url).toBe(
    "https://edge.s3.us-east-1.amazonaws.com/assets/shop/prod/web/r1/_next/static/app.js",
  );
  expect(seen[0]?.headers.get("authorization")).toMatch(/^AWS4-HMAC-SHA256 Credential=ASIATASK\//);
  expect(seen[0]?.headers.get("x-amz-security-token")).toBe("task-session");
});

test("a container built from its function dispatches its own paths and keeps a shared cache", () => {
  const env = { OCEL_ORIGIN_DISPATCH: "1", OCEL_ISR: "1", AWS_REGION: "us-east-1" };

  expect(
    refuseIncompleteHost(newAwsFunctionContainerHost(env, taskCredentials), env),
  ).toBeUndefined();
});
