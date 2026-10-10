export interface ServeProps {
  kind: "object" | "origin";
  host: string;
  app: string;
  release: string;
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

export function buildOriginCall(originUrl: string, props: Omit<ServeProps, "kind">): ServeCall {
  return { url: originUrl, props: { ...props, kind: "origin" } };
}

export function buildServeRequest(url: string, visitor?: Request): Request {
  const headers = new Headers(visitor?.headers);
  for (const name of VISITOR_CREDENTIAL_HEADERS) headers.delete(name);
  return new Request(url, { method: visitor?.method ?? "GET", headers });
}

export function parseObjectKey(url: string): string {
  return new URL(url).pathname.slice(1).split("/").map(decodeURIComponent).join("/");
}
