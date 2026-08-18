import type { Route } from "@next/routing";

import type { RouteDeps } from "../src/index.mjs";
import type { AssetBucket } from "../src/assets.mjs";

export function assetStoreServing(
  files: Record<string, string>,
  probes?: string[],
  basePath = "",
): RouteDeps["assetStore"] {
  const store: AssetBucket = {
    async get(key) {
      probes?.push(key);
      const body = files[key];
      if (body === undefined) return null;
      return { body: new Blob([body]).stream() };
    },
  };
  return {
    store,
    assetPrefix: "",
    basePath,
    cache: { match: async () => undefined, put: async () => {} },
    waitUntil: () => {},
  };
}

export function internalRedirects(trailingSlash: boolean, basePath = ""): unknown[] {
  if (trailingSlash) {
    return [
      ...(basePath
        ? [
            {
              sourceRegex: `^${basePath}$`,
              headers: { Location: `${basePath}/` },
              status: 308,
              priority: true,
            },
          ]
        : []),
      {
        sourceRegex: `^${basePath}/((?!\\.well-known(?:/.*)?)(?:[^/]+/)*[^/]+\\.\\w+)/$`,
        headers: { Location: `${basePath}/$1` },
        missing: [{ type: "header", key: "x-nextjs-data" }],
        status: 308,
        priority: true,
      },
      {
        sourceRegex: `^${basePath}/((?!\\.well-known(?:/.*)?)(?:[^/]+/)*[^/\\.]+)$`,
        headers: { Location: `${basePath}/$1/` },
        status: 308,
        priority: true,
      },
    ];
  }
  return [
    ...(basePath
      ? [
          {
            sourceRegex: `^${basePath}/$`,
            headers: { Location: basePath },
            status: 308,
            priority: true,
          },
        ]
      : []),
    {
      sourceRegex: `^${basePath}/(.+?)/$`,
      headers: { Location: `${basePath}/$1` },
      status: 308,
      priority: true,
    },
  ];
}

export const SERVICE_WORKER_PATH = "/_next/static/service-worker/sw.js";
export const USER_REDIRECT_FROM = "/old.txt";

export function survivingRoutes(basePath = ""): unknown[] {
  return [
    {
      sourceRegex: `^${basePath}/_next/static/service-worker/(.*)$`,
      headers: { "Service-Worker-Allowed": basePath || "/" },
      priority: true,
    },
    {
      sourceRegex: `^${basePath}${USER_REDIRECT_FROM}(?:/)?$`,
      headers: { Location: `${basePath}/a/` },
      status: 308,
    },
  ];
}

export function manifestRoutes(
  trailingSlash: boolean,
  basePath = "",
  headerRoutes: unknown[] = [],
): Route[] {
  return [
    ...headerRoutes,
    ...internalRedirects(trailingSlash, basePath),
    ...survivingRoutes(basePath),
  ] as Route[];
}

export interface Scenario {
  trailingSlash?: boolean;
  skipTrailingSlashRedirect?: boolean;
  basePath?: string;
  pages?: string[];
  files?: Record<string, string>;
  edge?: RouteDeps["edge"];
  middleware?: { entryKey: string; matchers?: { sourceRegex: string }[] };
  dispatch?: RouteDeps["manifest"]["dispatch"];
  functionUrls?: RouteDeps["functionUrls"];
  fetch?: RouteDeps["fetch"];
  probes?: string[];
  headerRoutes?: unknown[];
}

export function deps(scenario: Scenario): RouteDeps {
  const basePath = scenario.basePath ?? "";
  const pages = scenario.pages ?? [];
  return {
    manifest: {
      entry: "",
      buildId: "t",
      basePath,
      trailingSlash: scenario.trailingSlash,
      skipTrailingSlashRedirect: scenario.skipTrailingSlashRedirect,
      pathnames: pages,
      routes: {
        beforeMiddleware: manifestRoutes(
          !!scenario.trailingSlash,
          basePath,
          scenario.headerRoutes,
        ),
        beforeFiles: [],
        afterFiles: [],
        dynamicRoutes: [],
        onMatch: [],
        fallback: [],
      },
      dispatch:
        scenario.dispatch ??
        Object.fromEntries(pages.map((path) => [path, { kind: "static" as const }])),
      middleware: scenario.middleware,
    },
    functionUrls: scenario.functionUrls ?? {},
    slug: "p1",
    deploymentId: "d1",
    app: "web",
    assetStore: assetStoreServing(
      { "/404.html": "not found", ...(scenario.files ?? {}) },
      scenario.probes,
      basePath,
    ),
    edge: scenario.edge,
    fetch: scenario.fetch,
  };
}

export function get(path: string, headers: Record<string, string> = {}) {
  return new Request(`https://app.example${path}`, { redirect: "manual", headers });
}
