import { expect, test } from "vitest";
import { awsDispatchAccess } from "../src/next/dispatch-host.mjs";

const { isOriginThrottle } = awsDispatchAccess({});

test("a 429 Lambda answers with x-amzn-errortype is a throttle the router retries", async () => {
  const throttled = new Response(null, {
    status: 429,
    headers: { "x-amzn-errortype": "TooManyRequestsException" },
  });

  expect(await isOriginThrottle?.(throttled)).toBe(true);
});

test("a 429 the app authored is served, never retried", async () => {
  expect(await isOriginThrottle?.(new Response(null, { status: 429 }))).toBe(false);
});

test("another status that carries x-amzn-errortype is no throttle", async () => {
  const failed = new Response(null, {
    status: 500,
    headers: { "x-amzn-errortype": "InternalError" },
  });

  expect(await isOriginThrottle?.(failed)).toBe(false);
});
