import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import type { NextRouteTable } from "@framework/next-protocol/route-table";
import { afterAll, beforeAll, expect, test } from "vitest";
import { readGcpDispatchHost } from "../src/next/dispatch-host.mjs";

const manifest: NextRouteTable = {
  rootFunction: "bundle-0",
  buildId: "b1",
  basePath: "",
  pathnames: [],
  routes: {
    beforeMiddleware: [],
    beforeFiles: [],
    afterFiles: [],
    dynamicRoutes: [],
    onMatch: [],
    fallback: [],
  },
  dispatch: {},
};

let dir: string;

beforeAll(async () => {
  dir = await mkdtemp(join(tmpdir(), "ocel-gcp-throttle-"));
  await writeFile(join(dir, "routing.json"), JSON.stringify(manifest));
});

afterAll(() => rm(dir, { recursive: true, force: true }));

function isOriginThrottle() {
  const host = readGcpDispatchHost(
    {
      OCEL_NEXT_ROUTE_TABLE: join(dir, "routing.json"),
      OCEL_STORAGE_COORDINATE: "shop/prod/web/r1a2b3c4d",
    },
    "http://127.0.0.1:1",
  );
  return host.isOriginThrottle;
}

test("a 429 Cloud Run answers for want of an instance is a throttle the router retries", async () => {
  const throttled = new Response(
    "The request was aborted because there was no available instance.",
    { status: 429 },
  );

  expect(await isOriginThrottle()?.(throttled)).toBe(true);
});

test("a 429 the app authored is served, never retried, with its body intact", async () => {
  const authored = new Response("slow down", { status: 429 });

  expect(await isOriginThrottle()?.(authored)).toBe(false);
  expect(await authored.text()).toBe("slow down");
});

test("another status with Cloud Run's text is no throttle", async () => {
  const failed = new Response("The request was aborted because there was no available instance.", {
    status: 500,
  });

  expect(await isOriginThrottle()?.(failed)).toBe(false);
});
