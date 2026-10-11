export interface ServeProps {
  kind: "object" | "origin";
  host: string;
  app: string;
  release: string;
  origin?: string;
  variant?: string;
}

export interface ServeCall {
  url: string;
  props: ServeProps;
}

const SERVE_ORIGIN = "https://serve.ocel.invalid";

const STORAGE_PREFIX_SEGMENTS = 4;

const VISITOR_CREDENTIAL_HEADERS = ["cookie", "authorization"] as const;

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

export function buildOriginCall(
  originUrl: string,
  props: Omit<ServeProps, "kind" | "origin">,
): ServeCall {
  const { origin, pathname, search } = new URL(originUrl);
  return {
    url: `${SERVE_ORIGIN}${pathname}${search}`,
    props: { ...props, kind: "origin", origin },
  };
}

export function buildOriginUrl(requestUrl: string, origin: string): string {
  const { pathname, search } = new URL(requestUrl);
  return `${origin}${pathname}${search}`;
}

export function buildServeRequest(url: string, visitor?: Request): Request {
  const headers = new Headers(visitor?.headers);
  for (const name of VISITOR_CREDENTIAL_HEADERS) headers.delete(name);
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
