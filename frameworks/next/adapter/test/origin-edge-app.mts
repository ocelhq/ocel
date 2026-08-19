import type { IncomingMessage, ServerResponse } from "node:http";
import type { AddressInfo } from "node:net";
import { createServer } from "node:http";
import { mkdir, mkdtemp, readFile, writeFile } from "node:fs/promises";
import { createRequire } from "node:module";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { vi } from "vitest";

import adapter from "../src/next-adapter.mjs";
import { defaultImages } from "./fixtures.mjs";

export interface OriginEdgeAppOptions {
  edgeKind?: string;
  allowDegraded?: string;
  middlewareEntryKey?: string;
  middlewareHandler?: string;
  edgeRouteEntryKey?: string;
  edgeRouteHandler?: string;
  edgeAssets?: Record<string, string>;
  edgePrerender?: string;
}

export interface BuiltMiddleware {
  runtime: string;
  id: string;
  entryKey: string;
  matchers?: { source: string; sourceRegex: string }[];
}

export interface BuiltManifest {
  entry: string;
  middleware?: BuiltMiddleware;
  dispatch: Record<string, Record<string, unknown>>;
}

export interface BuiltServe {
  needs: Record<string, { routes?: string[] }>;
}

export interface Dispatch {
  handler: (
    req: IncomingMessage,
    res: ServerResponse,
    ctx?: { waitUntil?: (promise: Promise<unknown>) => void },
  ) => unknown;
}

export interface OriginEdgeApp {
  projectDir: string;
  outputDir: string;
  manifest: BuiltManifest;
  serve: BuiltServe;
  hasEdgeBundle: boolean;
  funcDir: (id: string) => string;
  dispatchIn: (id: string) => Dispatch;
}

const defaultMiddlewareHandler = `async (request) =>
  new Response(null, {
    headers: {
      "x-middleware-next": "1",
      "x-mw-url": request.url,
      "x-mw-self": String(globalThis.self === globalThis),
      "x-mw-build": String(process.env.__NEXT_BUILD_ID),
    },
  })`;

const defaultEdgeRouteHandler = `async () => new Response("edge route")`;

export async function buildOriginEdgeApp(
  options: OriginEdgeAppOptions = {},
): Promise<OriginEdgeApp> {
  const middlewareEntryKey =
    options.middlewareEntryKey ?? "middleware_middleware";
  const edgeRouteEntryKey =
    options.edgeRouteEntryKey ?? "middleware_app/api/edge/route";
  const edgeAssets = options.edgeAssets ?? {};

  const projectDir = await mkdtemp(join(tmpdir(), "ocel-origin-edge-"));
  const write = async (rel: string, body: string) => {
    const abs = join(projectDir, rel);
    await mkdir(dirname(abs), { recursive: true });
    await writeFile(abs, body);
    return abs;
  };

  const register = (entryKey: string, handler: string) =>
    [
      "globalThis._ENTRIES = globalThis._ENTRIES || {}",
      `globalThis._ENTRIES[${JSON.stringify(entryKey)}] = { handler: ${handler} }`,
      "",
    ].join("\n");

  const mwChunk = await write(
    ".next/server/edge/chunks/mw.js",
    register(
      middlewareEntryKey,
      options.middlewareHandler ?? defaultMiddlewareHandler,
    ),
  );
  const routeChunk = await write(
    ".next/server/edge/chunks/route.js",
    register(
      edgeRouteEntryKey,
      options.edgeRouteHandler ?? defaultEdgeRouteHandler,
    ),
  );
  const nodeHandler = await write(
    ".next/server/app/api/docs/route.js",
    [
      "module.exports = {",
      "  handler: (req, res) => {",
      '    res.setHeader("x-page-user", req.headers["x-user"] || "none")',
      '    res.end("page for " + (req.url || "/").split("?")[0])',
      "  },",
      "}",
      "",
    ].join("\n"),
  );

  const assetPaths: Record<string, string> = {};
  for (const [name, body] of Object.entries(edgeAssets)) {
    assetPaths[name] = await write(join(".next/server/edge", name), body);
  }

  await write(
    ".next/server/middleware-manifest.json",
    JSON.stringify({
      version: 3,
      middleware: { "/": { name: "middleware", assets: [] } },
      functions: {
        "/api/edge": {
          name: "app/api/edge/route",
          assets: Object.keys(edgeAssets).map((name) => ({ name })),
        },
      },
    }),
  );

  const args = {
    routing: {
      beforeMiddleware: [],
      beforeFiles: [],
      afterFiles: [],
      dynamicRoutes: [],
      onMatch: [],
      fallback: [],
    },
    outputs: {
      pages: [],
      pagesApi: [],
      appPages: [],
      appRoutes: [
        {
          pathname: "/api/docs",
          id: "app/api/docs/route",
          sourcePage: "/api/docs/route",
          assets: {},
          runtime: "nodejs",
          filePath: nodeHandler,
          config: {},
          type: "APP_ROUTE",
        },
        {
          pathname: "/api/edge",
          id: "app/api/edge/route",
          sourcePage: "/api/edge/route",
          assets: { "chunks/route.js": routeChunk, ...assetPaths },
          runtime: "edge",
          filePath: routeChunk,
          edgeRuntime: {
            modulePath: routeChunk,
            entryKey: edgeRouteEntryKey,
            handlerExport: "handler",
          },
          config: { env: { __NEXT_BUILD_ID: "test-build" } },
          type: "APP_ROUTE",
        },
      ],
      middleware: {
        pathname: "/middleware",
        id: "middleware",
        sourcePage: "/middleware",
        assets: { "chunks/mw.js": mwChunk },
        runtime: "edge",
        filePath: mwChunk,
        edgeRuntime: {
          modulePath: mwChunk,
          entryKey: middlewareEntryKey,
          handlerExport: "handler",
        },
        config: {
          env: { __NEXT_BUILD_ID: "test-build" },
          matchers: [{ source: "/:path*", sourceRegex: "^(?:/(.*))?$" }],
        },
        type: "MIDDLEWARE",
      },
      staticFiles: [],
      prerenders: options.edgePrerender
        ? [
            {
              pathname: options.edgePrerender,
              id: options.edgePrerender,
              type: "PRERENDER",
              parentOutputId: "app/api/edge/route",
              groupId: 1,
              fallback: { filePath: undefined },
              config: {},
            },
          ]
        : [],
    },
    projectDir,
    repoRoot: projectDir,
    distDir: join(projectDir, ".next"),
    config: { basePath: "", images: defaultImages },
    nextVersion: "16.2.10",
    buildId: "test-build",
  };

  const outputDir = join(projectDir, ".ocel/output");
  vi.stubEnv("OCEL_OUTPUT_DIR", outputDir);
  vi.stubEnv("OCEL_EDGE_KIND", options.edgeKind ?? "cloudfront");
  vi.stubEnv("OCEL_ALLOW_DEGRADED", options.allowDegraded ?? "");

  await adapter.onBuildComplete!(args as never);

  const readJson = async (rel: string) =>
    JSON.parse(await readFile(join(outputDir, rel), "utf8"));

  const funcDir = (id: string) => join(outputDir, "functions", `${id}.func`);

  return {
    projectDir,
    outputDir,
    manifest: (await readJson("routing-manifest.json")) as BuiltManifest,
    serve: (await readJson("serve.json")) as BuiltServe,
    hasEdgeBundle: await exists(join(outputDir, "edge/bundle.json")),
    funcDir,
    dispatchIn: (id: string) => {
      const dir = funcDir(id);
      const load = createRequire(join(dir, "index.cjs"));
      return load("./__next_launcher.cjs") as Dispatch;
    },
  };
}

export interface RunningServer {
  origin: string;
  close: () => Promise<void>;
}

export async function listenOn(
  listener: (req: IncomingMessage, res: ServerResponse) => void,
): Promise<RunningServer> {
  const server = createServer(listener);
  await new Promise<void>((resolve) => {
    server.listen(0, "127.0.0.1", resolve);
  });
  const { port } = server.address() as AddressInfo;
  return {
    origin: `http://127.0.0.1:${port}`,
    close: () =>
      new Promise<void>((resolve) => {
        server.closeAllConnections();
        server.close(() => resolve());
      }),
  };
}

export function serveDispatch(dispatch: Dispatch): Promise<RunningServer> {
  return listenOn((req, res) => {
    Promise.resolve(dispatch.handler(req, res, { waitUntil: () => {} })).catch(
      (error: unknown) => {
        res.statusCode = 500;
        res.end(String(error));
      },
    );
  });
}

async function exists(path: string): Promise<boolean> {
  try {
    await readFile(path);
    return true;
  } catch {
    return false;
  }
}
