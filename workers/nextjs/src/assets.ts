// Static-asset serving (ADR 0002): the frozen worker's static output no
// longer lives behind a per-script Workers Assets binding — a script version
// that must survive every rollback the deployments store points at — but in
// the account-global R2 cache store, under this Deployment's own build-id-
// scoped prefix (assets/<project>/<app>/<build id>, disjoint from the isr/
// cache-entry prefix). A rollback swaps the asset set along with the
// active-deployment pointer, simply by reading a different prefix.

const IMMUTABLE_CACHE_CONTROL = "public, max-age=31536000, immutable";
const REVALIDATE_CACHE_CONTROL = "public, max-age=0, must-revalidate";

// One object as the R2 binding hands it back: a stream and, when present, its
// etag — exactly the shape serveStaticAsset needs. Nothing the store was
// written with is read back: contentTypeFor below is the single source of
// truth for what an asset's bytes are, so a deploy cannot stamp a type that
// disagrees with the one this worker would serve.
export interface AssetObject {
  body: ReadableStream | null;
  httpEtag?: string;
}

// The R2 bucket as this file needs it — the Cloudflare R2 binding satisfies
// it directly, so nothing here names an edge.
export interface AssetBucket {
  get(key: string): Promise<AssetObject | null>;
}

export interface AssetStoreDeps {
  // Absent when the substrate provisioned no cache store: every static route
  // then answers 404, the same posture an unadopted ISR store leaves
  // prerendering in.
  store?: AssetBucket;
  // This Deployment's own R2 key root (record.assetPrefix), joined directly
  // with a request's pathname to form the object key.
  assetPrefix: string;
  // The app's basePath (e.g. "/docs"), needed only to recognize a request for
  // basePath's own root — Next stores that document as `<basePath>/index.html`,
  // not `<basePath>.html`, and nothing else this store does is basePath-aware.
  // Absent (and "/" alone) covers the no-basePath app.
  basePath?: string;
  // The PoP cache fronting the R2 read. Bound to caches.default in
  // production; a no-op on *.workers.dev (this feature never runs there) but
  // functional on the custom domain this feature targets.
  cache: Pick<Cache, "match" | "put">;
  waitUntil: (promise: Promise<unknown>) => void;
}

// A Map, not an object literal: these are looked up by a name taken off the
// request, and an object literal would answer `constructor` with a function.
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

// Next types a file-based metadata route by its NAME, not its extension
// (getContentType in packages/next/src/build/webpack/loaders/
// next-metadata-route-loader.ts), and the two answers differ: app/manifest.json
// is a web manifest rather than JSON, and app/robots.txt is bare text/plain
// where a public/ .txt served through send() carries a charset.
//
// Which of Next's two rules applies turns on the directory a file came from,
// and a request carries only its name — so a public/ file that happens to be
// named like a metadata route is typed as the metadata route. That is the side
// worth being right on: the metadata route is the one Next's own suite asserts,
// and a public/robots.txt is served by the same bytes either way.
const METADATA_CONTENT_TYPES = table({
  "robots.txt": "text/plain",
  "manifest.json": "application/manifest+json",
});

// contentTypeFor infers a static file's content-type from the name the build
// emitted it under, and is the only thing that decides one: the R2 store holds
// raw bytes, and the deploy deliberately stamps no type of its own, so this
// table cannot be contradicted by a second one.
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

// cacheControlFor mirrors Next.js's own runtime rule for a statically served
// file (the nextStaticFolder branch of packages/next/src/server/lib/
// router-server.ts): immutable is its *else* case, earned only by the
// content-hashed chunks under _next/static. The service worker is exempt even
// there — it is the one chunk Next publishes at a build-invariant URL, so an
// immutable response would pin a visitor to one deploy's worker for a year and
// no later deploy could replace it. Everything else this store serves — public/
// files, the file-based metadata routes, prerendered documents — answers a
// stable URL and must be revalidated.
//
// The _next/static segment is matched wherever it appears rather than only at
// the head: a basePath app serves the same files under /<basePath>/_next/
// static/, and _next is a segment Next reserves, so nothing else can carry it.
export function cacheControlFor(pathname: string): string {
  const at = pathname.indexOf(NEXT_STATIC_PREFIX);
  if (at === -1) return REVALIDATE_CACHE_CONTROL;
  const itemPath = pathname.slice(at + NEXT_STATIC_PREFIX.length);
  return itemPath.startsWith(SERVICE_WORKER_PREFIX)
    ? REVALIDATE_CACHE_CONTROL
    : IMMUTABLE_CACHE_CONTROL;
}

// Where an object answering this request may be stored, likeliest first. The
// build writes a prerendered document under its .html name while the route it
// answers is spelled as a route — usually with no extension (/some, /404), but
// a route may carry what only looks like one (/v1.0) — so both names are tried,
// in the order that costs one read for what each kind of request nearly always
// is. An extensionless request is a page, so the document comes first; the
// request's own name still follows it, which is what serves an extensionless
// public/ file, and what serves a build made before documents were named. A
// request that names a file is nearly always that file, so it comes first
// there. A request already asking for .html can only be itself.
//
// The root — "/", or basePath's own root ("/docs" with no trailing segment) —
// is the one case where the request name and the document name are not the
// same string with ".html" appended: Next names the root document `index.html`
// (the same normalizePagePath rule the build-complete adapter fix undoes for
// routing — see routePathname in next-adapter.mts), so the candidate here is
// "<root>/index.html" instead of "<root>.html".
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

// A request that matches nothing — no route, no asset — is answered with the
// app's own rendered 404 page, which the build emits as static/404.html (App
// Router not-found.js and Pages Router 404.js alike) and the deploy uploads
// beside every other asset. A pages-router i18n build emits that document once
// per locale and never a bare one, so the locale the request resolved to is
// tried first. Only a build that emitted neither falls back to a bare body.
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

// serveStaticAsset answers a static-asset request from the R2 cache store,
// fronted by the colo Cache API so a hot asset costs no R2 read at all — for
// as long as the asset's own cache policy (cacheControlFor) lets the colo hold
// it. Always returns a Response (never throws): a miss, or no store bound at
// all, is the app's 404 page.
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
    // Inferred from the name the object is STORED under, never the request's:
    // the request names a route, and only the stored name says what the bytes
    // are.
    "content-type": contentTypeFor(hit.pathname),
    "cache-control": cacheControl,
  });
  if (object.httpEtag) headers.set("etag", object.httpEtag);

  if (object.httpEtag && matchesEtag(request, object.httpEtag)) {
    await object.body.cancel();
    return new Response(null, { status: 304, headers });
  }

  const response = new Response(object.body, { status: 200, headers });
  // Only an immutable asset is worth writing: anything else is stale the moment
  // it lands, so the colo would never answer from it.
  if (cacheControl === IMMUTABLE_CACHE_CONTROL) {
    deps.waitUntil(deps.cache.put(request, response.clone()));
  }
  return response;
}

// Whether the client already holds this exact object, per RFC 9110's
// If-None-Match: a bare "*", or any member of the list matching the object's
// etag once both sides are compared weakly.
function matchesEtag(request: Request, etag: string): boolean {
  const header = request.headers.get("if-none-match");
  if (!header) return false;
  if (header.trim() === "*") return true;
  const strong = (tag: string) => tag.trim().replace(/^W\//, "");
  return header.split(",").some((tag) => strong(tag) === strong(etag));
}
