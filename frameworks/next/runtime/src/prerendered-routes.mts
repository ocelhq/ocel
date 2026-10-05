import { routeOf } from "./cache-shaping.mjs";
import { type ProjectManifest, walkPrerender } from "./project-manifest.mjs";

export interface PrerenderedRoute {
  revalidate: number | false;
  partiallyStatic: boolean;
  hasPrefetchData: boolean;
}

export interface PrerenderedRoutes {
  basePath: string;
  cacheComponents: boolean;
  routes: ReadonlyMap<string, PrerenderedRoute>;
  dynamicRoutes: readonly { pattern: RegExp; route: PrerenderedRoute }[];
}

function revalidateOf(seconds: unknown): number | false {
  return typeof seconds === "number" && seconds > 0 ? seconds : false;
}

export function prerenderedRoutes(manifest: ProjectManifest | null): PrerenderedRoutes {
  const { basePath, routes, dynamicRoutes } = walkPrerender<PrerenderedRoute, PrerenderedRoute>(
    manifest,
    (entry) => ({
      revalidate: revalidateOf(entry?.initialRevalidateSeconds),
      partiallyStatic: entry?.renderingMode === "PARTIALLY_STATIC",
      hasPrefetchData: Boolean(entry?.prefetchDataRoute),
    }),
    (entry) => ({
      revalidate: revalidateOf(entry.fallbackRevalidate),
      partiallyStatic: entry.renderingMode === "PARTIALLY_STATIC",
      hasPrefetchData: false,
    }),
  );
  return {
    basePath,
    cacheComponents: manifest?.config?.cacheComponents === true,
    routes,
    dynamicRoutes: dynamicRoutes.map(({ pattern, value }) => ({ pattern, route: value })),
  };
}

export function findPrerenderedRoute(
  url: string | undefined,
  routes: PrerenderedRoutes,
): PrerenderedRoute | undefined {
  const path = routeOf(url, routes.basePath);
  return (
    routes.routes.get(path) ?? routes.dynamicRoutes.find(({ pattern }) => pattern.test(path))?.route
  );
}
