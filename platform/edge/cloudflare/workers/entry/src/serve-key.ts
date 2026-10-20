import { parseObjectKey as parseStorageKey } from "@platform/edge-contract/storage-key";

export interface ObjectProps {
  kind: "object";
  host: string;
  app: string;
  release: string;
}

export interface RenderProps {
  kind: "render";
}

export type ServeProps = ObjectProps | RenderProps;

export const RENDER_PROPS: RenderProps = { kind: "render" };

export interface RenderCoordinates {
  host: string;
  app: string;
  release: string;
  variant?: string;
}

const NO_VARIANT = "-";

const EDGE_SOURCE = "edge:";

const IMAGE_SOURCE = "image:";

export const IMAGE_CALL_HEADER = "x-ocel-image-call";

export const RENDER_RELEASE_HEADER = "x-ocel-render-release";

export const SERVE_REFUSED_HEADER = "x-ocel-serve-refused";

export const SERVE_NO_SHELL_HEADER = "x-ocel-serve-no-shell";

export const SERVE_UNSHARED_HEADER = "x-ocel-serve-unshared";

export const SERVE_LOOKUP_ONLY_HEADER = "x-ocel-serve-lookup-only";

export const SERVE_MISS_HEADER = "x-ocel-serve-miss";

export interface StoredEntryCall {
  path: string;
  fallbackPath?: string;
  revalidate?: number;
  expire?: number;
  pathDeterminesQuery?: boolean;
}

export interface RenderRelease {
  slug: string;
  storageCoordinate: string;
  tags: string[];
  sharesFallbackShell?: boolean;
  honoursAppCacheHeaders?: boolean;
  entry?: StoredEntryCall;
}

const RENDER_CALL_SEGMENTS = 5;

export interface ServeCall {
  url: string;
  props: ServeProps;
}

const SERVE_ORIGIN = "https://serve.ocel.invalid";

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

export function isReleaseObjectKey(key: string): boolean {
  return (parseStorageKey(key)?.rest ?? "") !== "";
}

export function parseKeyRelease(key: string): { app: string; release: string } {
  const parsed = parseStorageKey(key);
  if (parsed === undefined || parsed.rest === "") {
    throw new Error(`ocel: ${key} lies outside a release's storage prefix`);
  }
  return { app: parsed.app, release: parsed.release };
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
    props: RENDER_PROPS,
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

export function buildImageCall(
  optimizerUrl: string,
  coordinates: Omit<RenderCoordinates, "variant">,
  digest: string,
): ServeCall {
  return buildRenderCall(
    `${IMAGE_SOURCE}${optimizerUrl}`,
    `https://image.ocel/${digest}`,
    coordinates,
  );
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

export type RenderSource =
  | { kind: "origin"; url: string }
  | { kind: "edge"; entryKey: string; url: string }
  | { kind: "image"; optimizerUrl: string };

export interface RenderCall extends Required<RenderCoordinates> {
  source: RenderSource;
}

function readRenderSource(source: string, host: string, rest: string): RenderSource {
  if (source.startsWith(IMAGE_SOURCE)) {
    return { kind: "image", optimizerUrl: source.slice(IMAGE_SOURCE.length) };
  }
  if (source.startsWith(EDGE_SOURCE)) {
    return {
      kind: "edge",
      entryKey: source.slice(EDGE_SOURCE.length),
      url: `https://${host}${rest}`,
    };
  }
  return { kind: "origin", url: `${source}${rest}` };
}

export function parseRenderCall(requestUrl: string): RenderCall {
  const { scope, rest } = splitRenderCall(requestUrl);
  const [host, app, release, source, variant] = scope;
  return {
    host,
    app,
    release,
    variant: variant === NO_VARIANT ? "" : variant,
    source: readRenderSource(source, host, rest),
  };
}

export function buildServeRequest(url: string, visitor?: Request): Request {
  const headers = new Headers(visitor?.headers);
  for (const name of [
    ...VISITOR_CREDENTIAL_HEADERS,
    ...CACHE_KEYED_HEADERS,
    SERVE_LOOKUP_ONLY_HEADER,
    RENDER_RELEASE_HEADER,
  ]) {
    headers.delete(name);
  }
  return new Request(url, { method: "GET", headers, redirect: "manual" });
}

export function isRead(request: Request): boolean {
  return request.method === "GET" || request.method === "HEAD";
}

export function dropBodyForHead(visitor: Request, response: Response): Response {
  if (visitor.method !== "HEAD") return response;
  void response.body?.cancel();
  return new Response(null, response);
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

export function buildNextCanonicalUrl(url: URL, allowQuery?: string[]): string {
  const withoutRsc = new URL(url);
  withoutRsc.searchParams.delete("_rsc");
  return buildCanonicalUrl(withoutRsc, allowQuery);
}

export function buildCanonicalUrl(url: URL, allowQuery?: string[]): string {
  const canonical = new URL(url);
  canonical.search = "";
  const names = allowQuery ?? [...url.searchParams.keys()];
  for (const name of [...new Set(names)].sort()) {
    for (const value of url.searchParams.getAll(name)) canonical.searchParams.append(name, value);
  }
  return canonical.toString();
}
