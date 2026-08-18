import { afterEach, describe, expect, it, vi } from "vitest";

import { serve, type RouteDeps } from "../src/index.mjs";
import { assetStoreServing } from "../test-support/dispatch-scenario.mjs";
import type { TestRouteDeps } from "../test-support/dispatch-scenario.mjs";
import {
  buildOriginEdgeApp,
  serveDispatch,
  type OriginEdgeApp,
  type RunningServer,
} from "../../adapter/test/origin-edge-app.mjs";

const PAGE_ENTRY = "app/api/docs/route";

const emptyRoutes = {
  beforeMiddleware: [],
  beforeFiles: [],
  afterFiles: [],
  dynamicRoutes: [],
  onMatch: [],
  fallback: [],
};

const programs: Record<string, string> = {
  "lets the request through": `async () =>
    new Response(null, { headers: { "x-middleware-next": "1" } })`,

  "rewrites to another route": `async () =>
    new Response(null, {
      headers: { "x-middleware-rewrite": "https://app.example/rewritten" },
    })`,

  "redirects with a cookie": `async () =>
    new Response(null, {
      status: 307,
      headers: {
        location: "https://app.example/login",
        "set-cookie": "session=; Max-Age=0",
      },
    })`,

  "answers the request itself": `async () =>
    new Response(JSON.stringify({ error: "nope" }), {
      status: 401,
      headers: { "content-type": "application/json" },
    })`,

  "overrides a request header the page then sees": `async () =>
    new Response(null, {
      headers: {
        "x-middleware-next": "1",
        "x-middleware-override-headers": "x-user",
        "x-middleware-request-x-user": "alice",
      },
    })`,
};

type EdgeHandler = (request: Request) => Promise<Response>;

interface EdgeEntry {
  handler: (request: Request, ctx: unknown) => Promise<Response>;
}

interface Hosted {
  app: OriginEdgeApp;
  origin: RunningServer;
  edge: EdgeHandler;
}

const running: RunningServer[] = [];

afterEach(async () => {
  await Promise.all(running.splice(0).map((server) => server.close()));
  vi.unstubAllEnvs();
});

let builds = 0;

async function host(program: string): Promise<Hosted> {
  const entryKey = `middleware_parity_${builds++}`;
  const app = await buildOriginEdgeApp({
    allowDegraded: "edge-middleware",
    middlewareEntryKey: entryKey,
    middlewareHandler: program,
  });
  const dispatch = app.dispatchIn(app.manifest.middleware!.id);
  const origin = await serveDispatch(dispatch);
  running.push(origin);

  const { _ENTRIES } = globalThis as unknown as {
    _ENTRIES: Record<string, EdgeEntry | Promise<EdgeEntry>>;
  };
  const registered = await _ENTRIES[entryKey]!;

  return {
    app,
    origin,
    edge: (request) =>
      registered.handler(request, {
        waitUntil: () => {},
        signal: request.signal,
        requestMeta: {},
      }),
  };
}

function bundleOf(hosted: Hosted): string {
  return hosted.app.manifest.middleware!.id;
}

function manifestFor(hosted: Hosted, middleware: unknown) {
  const target = {
    kind: "lambda",
    id: bundleOf(hosted),
    entryKey: PAGE_ENTRY,
  };
  return {
    entry: bundleOf(hosted),
    buildId: "test-build",
    basePath: "",
    pathnames: ["/dashboard", "/rewritten"],
    routes: emptyRoutes,
    dispatch: { "/dashboard": target, "/rewritten": target },
    middleware,
  };
}

function depsFor(hosted: Hosted, overrides: TestRouteDeps): RouteDeps {
  return {
    slug: "p1",
    deploymentId: "d1",
    app: "web",
    assetStore: assetStoreServing({}),
    functionUrls: { [bundleOf(hosted)]: hosted.origin.origin },
    ...overrides,
  } as RouteDeps;
}

function edgeDeps(hosted: Hosted): RouteDeps {
  return depsFor(hosted, {
    manifest: manifestFor(hosted, {
      runtime: "edge",
      entryKey: "mw",
    }) as never,
    edge: (async (_entryKey: string, request: Request) =>
      hosted.edge(request)) as RouteDeps["edge"],
  });
}

function originDeps(hosted: Hosted): RouteDeps {
  return depsFor(hosted, {
    manifest: manifestFor(hosted, hosted.app.manifest.middleware) as never,
  });
}

async function shape(response: Response) {
  return {
    status: response.status,
    location: response.headers.get("location"),
    contentType: response.headers.get("content-type"),
    setCookie: response.headers.getSetCookie(),
    pageUser: response.headers.get("x-page-user"),
    body: await response.text(),
  };
}

describe("a waived edge middleware compiled as a Node entry", () => {
  for (const [name, program] of Object.entries(programs)) {
    it(`${name}, exactly as the Cloudflare edge does`, async () => {
      const hosted = await host(program);
      expect(hosted.app.manifest.middleware).toMatchObject({
        runtime: "nodejs",
        entryKey: "/_middleware",
      });

      const request = () => new Request("https://app.example/dashboard");
      const onEdge = await serve(request(), edgeDeps(hosted));
      const inProcess = await serve(request(), originDeps(hosted));

      expect(await shape(inProcess)).toEqual(await shape(onEdge));
    });
  }

  it("reaches the middleware over the deployment's own origin, not a sibling", async () => {
    const hosted = await host(programs["lets the request through"]!);
    const deps = originDeps(hosted);
    const seen: string[] = [];
    deps.fetch = (async (input: Request) => {
      seen.push(new URL(input.url).origin);
      return fetch(input);
    }) as unknown as typeof fetch;

    const response = await serve(
      new Request("https://app.example/dashboard"),
      deps,
    );

    expect(response.status).toBe(200);
    expect(seen).toEqual([hosted.origin.origin, hosted.origin.origin]);
  });
});
