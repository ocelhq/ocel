import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { extname, join } from "node:path";

export const HOSTING_VERSION = 1;

export const FRAMEWORK = "sveltekit";

export const ROOT_FUNCTION = "/";

export interface StaticRules {
  immutablePrefixes: string[];
  mustRevalidatePrefixes?: string[];
}

export interface Hosting {
  version: typeof HOSTING_VERSION;
  framework: typeof FRAMEWORK;
  frameworkBuildId: string;
  rootFunction: string;
  static: StaticRules;
  needs: Record<string, never>;
}

export interface Asset {
  file: string;
  type: string;
  size: number;
  etag: string;
}

export interface Redirect {
  status: number;
  location: string;
}

export interface Served {
  base: string;
  immutable: string[];
  assets: Record<string, Asset>;
  redirects: Record<string, Redirect>;
}

export function describeStaticRules(appPath: string): StaticRules {
  return { immutablePrefixes: [`/${appPath}/immutable/`] };
}

export function describeHosting(buildId: string, rules: StaticRules): Hosting {
  return {
    version: HOSTING_VERSION,
    framework: FRAMEWORK,
    frameworkBuildId: buildId,
    rootFunction: ROOT_FUNCTION,
    static: rules,
    needs: {},
  };
}

const FALLBACK_TYPES: Record<string, string> = {
  ".html": "text/html",
  ".js": "text/javascript",
  ".mjs": "text/javascript",
  ".css": "text/css",
  ".json": "application/json",
  ".map": "application/json",
  ".svg": "image/svg+xml",
  ".png": "image/png",
  ".jpg": "image/jpeg",
  ".jpeg": "image/jpeg",
  ".gif": "image/gif",
  ".webp": "image/webp",
  ".avif": "image/avif",
  ".ico": "image/x-icon",
  ".woff": "font/woff",
  ".woff2": "font/woff2",
  ".txt": "text/plain",
  ".xml": "application/xml",
  ".webmanifest": "application/manifest+json",
  ".wasm": "application/wasm",
};

export function findContentType(file: string, mimeTypes: Record<string, string> = {}): string {
  const extension = extname(file).toLowerCase();
  const type = mimeTypes[extension] ?? FALLBACK_TYPES[extension] ?? "application/octet-stream";
  return type.startsWith("text/") || type === "application/json" || type.endsWith("+json")
    ? `${type}; charset=utf-8`
    : type;
}

function isHidden(file: string): boolean {
  return (
    file.split("/").some((segment) => segment.startsWith(".")) && !file.startsWith(".well-known/")
  );
}

function measure(root: string, file: string, mimeTypes: Record<string, string>): Asset {
  const body = readFileSync(join(root, file));
  return {
    file,
    type: findContentType(file, mimeTypes),
    size: body.byteLength,
    etag: `"${createHash("sha256").update(body).digest("base64url")}"`,
  };
}

export interface ServedInput {
  root: string;
  base: string;
  appPath: string;
  clientFiles: string[];
  prerenderedFiles: string[];
  prerenderedPaths: string[];
  redirects: Map<string, Redirect>;
  mimeTypes?: Record<string, string>;
}

export function tableServedFiles(input: ServedInput): Served {
  const mimeTypes = input.mimeTypes ?? {};
  const assets: Record<string, Asset> = {};
  const claim = (pathname: string, asset: Asset) => {
    if (!(pathname in assets)) assets[pathname] = asset;
  };

  const client = input.clientFiles
    .filter((file) => !isHidden(file))
    .sort()
    .map((file) => measure(join(input.root, input.base), file, mimeTypes));
  for (const asset of client) claim(`${input.base}/${asset.file}`, asset);
  for (const asset of client) {
    if (!asset.file.endsWith(".html")) continue;
    const index = asset.file === "index.html" || asset.file.endsWith("/index.html");
    const withSlash = index
      ? `${input.base}/${asset.file.slice(0, -"index.html".length)}`
      : `${input.base}/${asset.file.slice(0, -".html".length)}/`;
    claim(withSlash, asset);
    if (withSlash.length > 1) claim(withSlash.slice(0, -1), asset);
  }

  const prerendered = new Map(
    input.prerenderedFiles
      .filter((file) => !isHidden(file))
      .map((file) => [file, measure(join(input.root, input.base), file, mimeTypes)] as const),
  );
  for (const pathname of input.prerenderedPaths) {
    const file = pathname.slice(input.base.length + 1) || "index.html";
    const asset =
      prerendered.get(file) ??
      prerendered.get(file + (file.endsWith("/") ? "index.html" : ".html"));
    if (asset) assets[pathname] = asset;
  }

  return {
    base: input.base,
    immutable: describeStaticRules(input.appPath).immutablePrefixes,
    assets,
    redirects: Object.fromEntries(input.redirects),
  };
}
