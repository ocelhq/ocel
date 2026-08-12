const IMMUTABLE_CACHE_CONTROL = "public, max-age=31536000, immutable";
const REVALIDATE_CACHE_CONTROL = "public, max-age=0, must-revalidate";

export interface AssetObject {
  body: ReadableStream | null;
  httpEtag?: string;
}

export interface AssetBucket {
  get(key: string): Promise<AssetObject | null>;
}

export interface AssetStoreDeps {
  store?: AssetBucket;
  assetPrefix: string;
  basePath?: string;
  cache: Pick<Cache, "match" | "put">;
  waitUntil: (promise: Promise<unknown>) => void;
}

function table(entries: Record<string, string>): Map<string, string> {
  return new Map(Object.entries(entries));
}

const CONTENT_TYPES = table({
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
  ".mjs": "text/javascript; charset=utf-8",
  ".css": "text/css; charset=utf-8",
  ".json": "application/json; charset=utf-8",
  ".map": "application/json; charset=utf-8",
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
  ".ttf": "font/ttf",
  ".eot": "application/vnd.ms-fontobject",
  ".txt": "text/plain; charset=utf-8",
  ".xml": "application/xml",
  ".webmanifest": "application/manifest+json",
  ".wasm": "application/wasm",
});

const METADATA_CONTENT_TYPES = table({
  "robots.txt": "text/plain",
  "manifest.json": "application/manifest+json",
});

export function contentTypeFor(pathname: string): string {
  const name = pathname.slice(pathname.lastIndexOf("/") + 1).toLowerCase();
  const metadata = METADATA_CONTENT_TYPES.get(name);
  if (metadata) return metadata;
  const dot = name.lastIndexOf(".");
  if (dot === -1) return "application/octet-stream";
  return CONTENT_TYPES.get(name.slice(dot)) ?? "application/octet-stream";
}

const NEXT_STATIC_PREFIX = "/_next/static/";
const SERVICE_WORKER_PREFIX = "service-worker/";

export function isNextStaticPathname(pathname: string): boolean {
  return pathname.includes(NEXT_STATIC_PREFIX);
}

export function cacheControlFor(pathname: string): string {
  const at = pathname.indexOf(NEXT_STATIC_PREFIX);
  if (at === -1) return REVALIDATE_CACHE_CONTROL;
  const itemPath = pathname.slice(at + NEXT_STATIC_PREFIX.length);
  return itemPath.startsWith(SERVICE_WORKER_PREFIX)
    ? REVALIDATE_CACHE_CONTROL
    : IMMUTABLE_CACHE_CONTROL;
}

function storedPathnames(pathname: string, basePath = ""): string[] {
  if (pathname.endsWith(".html")) return [pathname];
  if (pathname === "/" || pathname === basePath) {
    return [
      pathname === "/" ? "/index.html" : `${pathname}/index.html`,
      pathname,
    ];
  }
  const document = `${pathname}.html`;
  return hasExtension(pathname) ? [pathname, document] : [document, pathname];
}

function hasExtension(pathname: string): boolean {
  return pathname.slice(pathname.lastIndexOf("/") + 1).includes(".");
}

async function notFound(deps: AssetStoreDeps, locale?: string): Promise<Response> {
  const names = locale ? [`/${locale}/404.html`, "/404.html"] : ["/404.html"];
  for (const name of names) {
    const page = await deps.store?.get(`${deps.assetPrefix}${name}`);
    if (page?.body) {
      return new Response(page.body, {
        status: 404,
        headers: { "content-type": "text/html; charset=utf-8" },
      });
    }
  }
  return new Response("Not Found", { status: 404 });
}

export async function serveStaticAsset(
  request: Request,
  url: URL,
  deps: AssetStoreDeps,
  locale?: string,
): Promise<Response> {
  if (!deps.store) return notFound(deps, locale);

  const cached = await deps.cache.match(request);
  if (cached) return cached;

  let hit:
    | { pathname: string; object: AssetObject & { body: ReadableStream } }
    | undefined;
  for (const pathname of storedPathnames(url.pathname, deps.basePath)) {
    const object = await deps.store.get(`${deps.assetPrefix}${pathname}`);
    if (object?.body) {
      hit = { pathname, object: { ...object, body: object.body } };
      break;
    }
  }
  if (!hit) return notFound(deps, locale);

  const { object } = hit;
  const cacheControl = cacheControlFor(hit.pathname);
  const headers = new Headers({
    "content-type": contentTypeFor(hit.pathname),
    "cache-control": cacheControl,
  });
  if (object.httpEtag) headers.set("etag", object.httpEtag);

  if (object.httpEtag && matchesEtag(request, object.httpEtag)) {
    await object.body.cancel();
    return new Response(null, { status: 304, headers });
  }

  const response = new Response(object.body, { status: 200, headers });
  if (cacheControl === IMMUTABLE_CACHE_CONTROL) {
    deps.waitUntil(deps.cache.put(request, response.clone()));
  }
  return response;
}

function matchesEtag(request: Request, etag: string): boolean {
  const header = request.headers.get("if-none-match");
  if (!header) return false;
  if (header.trim() === "*") return true;
  const strong = (tag: string) => tag.trim().replace(/^W\//, "");
  return header.split(",").some((tag) => strong(tag) === strong(etag));
}
