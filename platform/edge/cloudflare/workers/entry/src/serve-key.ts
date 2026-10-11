export interface ObjectProps {
  kind: "object";
  host: string;
  app: string;
  release: string;
}

export interface OriginPurgeScope {
  kind: "origin";
}

export type ServeProps = ObjectProps | OriginPurgeScope;

export const ORIGIN_PURGE_SCOPE: OriginPurgeScope = { kind: "origin" };

export interface RenderCoordinates {
  host: string;
  app: string;
  release: string;
  variant?: string;
}

const NO_VARIANT = "-";

const EDGE_SOURCE = "edge:";

export const RENDER_RELEASE_HEADER = "x-ocel-render-release";

export const SERVE_REFUSED_HEADER = "x-ocel-serve-refused";

export const SERVE_NO_SHELL_HEADER = "x-ocel-serve-no-shell";

export const SERVE_UNSHARED_HEADER = "x-ocel-serve-unshared";

export interface StoredEntryCall {
  path: string;
  fallbackPath?: string;
  revalidate?: number;
  expire?: number;
}

export interface RenderRelease {
  slug: string;
  isrPrefix: string;
  tags: string[];
  sharesFallbackShell?: boolean;
  entry?: StoredEntryCall;
}

const RENDER_CALL_SEGMENTS = 5;

export interface ServeCall {
  url: string;
  props: ServeProps;
}

const SERVE_ORIGIN = "https://serve.ocel.invalid";

const STORAGE_PREFIX_SEGMENTS = 4;

const VISITOR_CREDENTIAL_HEADERS = ["cookie", "authorization"] as const;

const CACHE_KEYED_HEADERS = [
  "x-forwarded-host",
  "x-host",
  "x-forwarded-scheme",
  "x-original-url",
  "x-rewrite-url",
  "forwarded",
  "x-http-method-override",
  "x-http-method",
  "x-method-override",
] as const;

export function parseKeyRelease(key: string): { app: string; release: string } {
  const segments = key.split("/");
  if (
    segments.length <= STORAGE_PREFIX_SEGMENTS ||
    segments.some((segment) => segment === "" || segment === "." || segment === "..")
  ) {
    throw new Error(`ocel: ${key} lies outside a release's storage prefix`);
  }
  return { app: segments[2], release: segments[3] };
}

export function encodeKeyPath(key: string): string {
  return key.split("/").map(encodeURIComponent).join("/");
}

export function buildObjectCall(host: string, key: string): ServeCall {
  const { app, release } = parseKeyRelease(key);
  return {
    url: `${SERVE_ORIGIN}/${encodeKeyPath(key)}`,
    props: { kind: "object", host, app, release },
  };
}

function buildRenderCall(source: string, url: string, coordinates: RenderCoordinates): ServeCall {
  const { pathname, search } = new URL(url);
  const { host, app, release, variant } = coordinates;
  const scope = [host, app, release, source, variant || NO_VARIANT].map(encodeURIComponent);
  return {
    url: `${SERVE_ORIGIN}/${scope.join("/")}${pathname}${search}`,
    props: ORIGIN_PURGE_SCOPE,
  };
}

export function buildOriginCall(originUrl: string, coordinates: RenderCoordinates): ServeCall {
  return buildRenderCall(new URL(originUrl).origin, originUrl, coordinates);
}

export function buildEdgeCall(
  publicUrl: string,
  coordinates: RenderCoordinates,
  entryKey: string,
): ServeCall {
  return buildRenderCall(`${EDGE_SOURCE}${entryKey}`, publicUrl, coordinates);
}

function splitRenderCall(requestUrl: string): { scope: string[]; rest: string } {
  const { pathname, search } = new URL(requestUrl);
  const segments = pathname.split("/");
  const scope = segments.slice(1, RENDER_CALL_SEGMENTS + 1);
  if (segments.length < RENDER_CALL_SEGMENTS + 2 || scope.includes("")) {
    throw new Error(
      `ocel: ${pathname} names no host, app, release, origin or edge entry, and variant`,
    );
  }
  return {
    scope: scope.map(decodeURIComponent),
    rest: `/${segments.slice(RENDER_CALL_SEGMENTS + 1).join("/")}${search}`,
  };
}

export function readRenderCall(requestUrl: string): Required<RenderCoordinates> {
  const [host, app, release, , variant] = splitRenderCall(requestUrl).scope;
  return { host, app, release, variant: variant === NO_VARIANT ? "" : variant };
}

export function buildOriginUrl(requestUrl: string): string {
  const { scope, rest } = splitRenderCall(requestUrl);
  return `${scope[3]}${rest}`;
}

export function readEdgeEntry(requestUrl: string): string | undefined {
  const source = splitRenderCall(requestUrl).scope[3];
  return source.startsWith(EDGE_SOURCE) ? source.slice(EDGE_SOURCE.length) : undefined;
}

export function buildEdgeUrl(requestUrl: string): string {
  const { scope, rest } = splitRenderCall(requestUrl);
  return `https://${scope[0]}${rest}`;
}

export function buildServeRequest(url: string, visitor?: Request): Request {
  const headers = new Headers(visitor?.headers);
  for (const name of [...VISITOR_CREDENTIAL_HEADERS, ...CACHE_KEYED_HEADERS]) headers.delete(name);
  return new Request(url, { method: visitor?.method ?? "GET", headers });
}

export function parseObjectKey(url: string): string {
  return new URL(url).pathname.slice(1).split("/").map(decodeURIComponent).join("/");
}

export function buildVariantPath(
  pathname: string,
  headers: Headers,
  renderingMode: "STATIC" | "PARTIALLY_STATIC" | undefined,
): string | null {
  if (headers.get("RSC") === null) return pathname;

  const nextUrl = headers.get("next-url");
  const base = nextUrl !== null ? `${pathname}.iu/${encodeURIComponent(nextUrl)}` : pathname;

  const segment = headers.get("next-router-segment-prefetch");
  if (segment !== null) {
    return `${base}.segments/${encodeURIComponent(segment)}.segment.rsc`;
  }

  const prefetch = headers.get("next-router-prefetch");
  if (prefetch === "1") return `${base}.prefetch.rsc`;
  if (prefetch !== null) return null;

  return renderingMode === "PARTIALLY_STATIC" ? null : `${base}.rsc`;
}

export function buildCanonicalUrl(url: URL, allowQuery?: string[]): string {
  const canonical = new URL(url);
  canonical.search = "";
  const names = (allowQuery ?? [...url.searchParams.keys()]).filter((name) => name !== "_rsc");
  for (const name of [...new Set(names)].sort()) {
    for (const value of url.searchParams.getAll(name)) canonical.searchParams.append(name, value);
  }
  return canonical.toString();
}
