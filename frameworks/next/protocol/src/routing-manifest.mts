import type { I18nConfig } from "@next/routing";

export type RouteHas =
  | {
      type: "header" | "cookie" | "query";
      key: string;
      value?: string;
    }
  | {
      type: "host";
      key?: undefined;
      value: string;
    };

export interface CompiledRemotePattern {
  protocol?: string;
  hostname: string;
  port?: string;
  pathname: string;
  search?: string;
}

export interface CompiledLocalPattern {
  pathname: string;
  search?: string;
}

export interface ImageConfig {
  path: string;
  deviceSizes: number[];
  imageSizes: number[];
  qualities?: number[];
  formats: string[];
  domains: string[];
  minimumCacheTTL: number;
  maximumRedirects: number;
  maximumResponseBody: number;
  dangerouslyAllowSVG: boolean;
  dangerouslyAllowLocalIP: boolean;
  contentSecurityPolicy: string;
  contentDispositionType: string;
  remotePatterns: CompiledRemotePattern[];
  localPatterns?: CompiledLocalPattern[];
  configHash: string;
}

export type DispatchTarget =
  | { kind: "static" }
  | {
      kind: "lambda";
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

export interface RoutingManifest {
  entry: string;
  buildId: string;
  appName?: string;
  basePath: string;
  trailingSlash?: boolean;
  skipTrailingSlashRedirect?: boolean;
  skipMiddlewareUrlNormalize?: boolean;
  pathnames: string[];
  routes: unknown;
  dispatch: Record<string, DispatchTarget>;
  errorRoutes?: {
    notFound?: string;
    notFoundFlight?: string;
    serverError?: string;
  };
  middleware?: MiddlewareEntry;
  images?: ImageConfig;
  i18n?: I18nConfig;
  assetHashes?: Record<string, string>;
  vercelCacheAlias?: boolean;
}
