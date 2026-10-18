import type { I18nConfig, ResolveRoutesParams, Route, RouteHas } from "@next/routing";
import type { ImageConfig } from "./image.mjs";

export type { RouteHas };

export type RoutingTable = Omit<ResolveRoutesParams["routes"], "beforeMiddleware"> & {
  beforeMiddleware: Array<Route & { priority?: boolean }>;
};

export type DispatchTarget =
  | { kind: "static" }
  | {
      kind: "function";
      id: string;
      entryKey?: string;
      parent?: string;
      revalidate?: unknown;
      page?: boolean;
    }
  | {
      kind: "prerender";
      id?: string;
      entryKey?: string;
      tags?: string[];
      allowQuery?: string[];
      fallback?: {
        initialExpiration?: number;
        initialRevalidate?: number | false;
      };
      pprChain?: { headers: Record<string, string> };
      edgeEntryKey?: string;
      config: {
        allowQuery?: string[];
        allowHeader?: string[];
        bypassFor?: RouteHas[];
        renderingMode?: "STATIC" | "PARTIALLY_STATIC";
        partialFallback?: boolean;
        bypassToken?: string;
      };
    }
  | { kind: "edge"; entryKey?: string };

export interface MiddlewareMatcher {
  sourceRegex: string;
  has?: RouteHas[];
  missing?: RouteHas[];
}

export type MiddlewareEntry =
  | { runtime?: "edge"; entryKey: string; matchers?: MiddlewareMatcher[] }
  | {
      runtime: "nodejs";
      id: string;
      entryKey: string;
      matchers?: MiddlewareMatcher[];
    };

export interface NextRouteTable {
  rootFunction: string;
  buildId: string;
  appName?: string;
  basePath: string;
  assetPrefixPathname?: string;
  trailingSlash?: boolean;
  skipTrailingSlashRedirect?: boolean;
  skipMiddlewareUrlNormalize?: boolean;
  pathnames: string[];
  routes: RoutingTable;
  dispatch: Record<string, DispatchTarget>;
  errorRoutes?: {
    notFound?: string;
    serverError?: string;
  };
  middleware?: MiddlewareEntry;
  images?: ImageConfig;
  i18n?: I18nConfig;
  assetHashes?: Record<string, string>;
}
