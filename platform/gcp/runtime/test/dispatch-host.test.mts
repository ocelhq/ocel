import { mkdtemp, rm, writeFile } from "node:fs/promises";
import http from "node:http";
import { tmpdir } from "node:os";
import { join } from "node:path";
import type { RoutingManifest } from "@framework/next-protocol/routing-manifest";
import { dispatchRequest } from "@framework/next-runtime/dispatch-host";
import { afterAll, beforeAll, expect, test } from "vitest";
import { readGcpDispatchHost } from "../src/next/dispatch-host.mjs";

const manifest: RoutingManifest = {
  entry: "bundle-0",
  buildId: "b1",
  basePath: "",
  pathnames: ["/home", "/other"],
  routes: {
    beforeMiddleware: [],
    beforeFiles: [],
    afterFiles: [],
    dynamicRoutes: [],
    onMatch: [],
    fallback: [],
  },
  dispatch: {
    "/home": { kind: "function", id: "bundle-0", entryKey: "/home" },
    "/other": { kind: "function", id: "bundle-1", entryKey: "/other" },
  },
};

let dir: string;
let local: http.Server;
let localOrigin: string;
const served: string[] = [];

beforeAll(async () => {
  dir = await mkdtemp(join(tmpdir(), "ocel-gcp-dispatch-"));
  await writeFile(join(dir, "routing.json"), JSON.stringify(manifest));
  local = http.createServer((req, res) => {
    served.push(String(req.headers["x-ocel-entry"]));
    res.end("rendered here");
  });
  await new Promise<void>((resolve) => local.listen({ host: "127.0.0.1", port: 0 }, resolve));
  const { port } = local.address() as { port: number };
  localOrigin = `http://127.0.0.1:${port}`;
});

afterAll(async () => {
  await new Promise<void>((resolve) => local.close(() => resolve()));
  await rm(dir, { recursive: true, force: true });
});

test("every function a route names is rendered by the instance that routed it", async () => {
  const host = readGcpDispatchHost(
    { OCEL_ROUTING_MANIFEST: join(dir, "routing.json") },
    localOrigin,
  );

  const responses = await Promise.all(
    ["/home", "/other"].map((path) =>
      dispatchRequest(new Request(`https://shop.example${path}`), host, () => {}),
    ),
  );

  expect(await Promise.all(responses.map((response) => response.text()))).toEqual([
    "rendered here",
    "rendered here",
  ]);
  expect(served.sort()).toEqual(["/home", "/other"]);
});
