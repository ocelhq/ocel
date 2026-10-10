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

export function releaseOfKey(key: string): { app: string; release: string } {
  const segments = key.split("/");
  if (
    segments.length <= STORAGE_PREFIX_SEGMENTS ||
    segments.some((segment) => segment === "" || segment === "." || segment === "..")
  ) {
    throw new Error(`ocel: ${key} lies outside a release's storage prefix`);
  }
  return { app: segments[2], release: segments[3] };
}

export function objectCall(host: string, key: string): ServeCall {
  const { app, release } = releaseOfKey(key);
  const path = key.split("/").map(encodeURIComponent).join("/");
  return { url: `${SERVE_ORIGIN}/${path}`, props: { kind: "object", host, app, release } };
}

export function objectKeyOf(url: string): string {
  return new URL(url).pathname.slice(1).split("/").map(decodeURIComponent).join("/");
}
