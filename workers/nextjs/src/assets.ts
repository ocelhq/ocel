// Static-asset serving (ADR 0002): the frozen worker's static output no
// longer lives behind a per-script Workers Assets binding — a script version
// that must survive every rollback the deployments store points at — but in
// the account-global R2 cache store, under this Deployment's own build-id-
// scoped prefix (assets/<project>/<app>/<build id>, disjoint from the isr/
// cache-entry prefix). A rollback swaps the asset set along with the
// active-deployment pointer, simply by reading a different prefix.

const IMMUTABLE_CACHE_CONTROL = "public, max-age=31536000, immutable";

// One object as the R2 binding hands it back: a stream, (when present) its
// etag, and the metadata it was written with — exactly the shape
// serveStaticAsset needs. The deploy now stamps each object's content-type at
// upload (via mime.TypeByExtension), so httpMetadata.contentType is the
// authoritative type; contentTypeFor is the fallback for legacy objects
// uploaded before that, and for extensions the deploy left unset.
export interface AssetObject {
  body: ReadableStream | null;
  httpEtag?: string;
  httpMetadata?: { contentType?: string };
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
  // The PoP cache fronting the R2 read. Bound to caches.default in
  // production; a no-op on *.workers.dev (this feature never runs there) but
  // functional on the custom domain this feature targets.
  cache: Pick<Cache, "match" | "put">;
  waitUntil: (promise: Promise<unknown>) => void;
}

const CONTENT_TYPES: Record<string, string> = {
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
  ".wasm": "application/wasm",
};

// contentTypeFor infers a static file's content-type from its path, mirroring
// the extension this Deployment's build emitted it under — the R2 store holds
// raw bytes with no content-type of its own to read back.
export function contentTypeFor(pathname: string): string {
  const dot = pathname.lastIndexOf(".");
  if (dot === -1) return "application/octet-stream";
  return CONTENT_TYPES[pathname.slice(dot).toLowerCase()] ?? "application/octet-stream";
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
function storedPathnames(pathname: string): string[] {
  if (pathname.endsWith(".html")) return [pathname];
  const document = `${pathname}.html`;
  return hasExtension(pathname) ? [pathname, document] : [document, pathname];
}

function hasExtension(pathname: string): boolean {
  return pathname.slice(pathname.lastIndexOf("/") + 1).includes(".");
}

// A request that matches nothing — no route, no asset — is answered with the
// app's own rendered 404 page, which the build emits as static/404.html (App
// Router not-found.js and Pages Router 404.js alike) and the deploy uploads
// beside every other asset. Only a build that emitted none falls back to a
// bare body.
async function notFound(deps: AssetStoreDeps): Promise<Response> {
  const page = await deps.store?.get(`${deps.assetPrefix}/404.html`);
  if (!page?.body) return new Response("Not Found", { status: 404 });
  return new Response(page.body, {
    status: 404,
    headers: { "content-type": "text/html; charset=utf-8" },
  });
}

// serveStaticAsset answers a static-asset request from the R2 cache store,
// fronted by the colo Cache API so a hot asset costs no R2 read at all. Every
// object this build could ever serve was written once, at its own
// build-id-scoped path — nothing at that key is ever overwritten — so a colo
// hit never needs revalidation: the response is cached with immutable
// headers. Always returns a Response (never throws): a miss, or no store
// bound at all, is the app's 404 page.
export async function serveStaticAsset(
  request: Request,
  url: URL,
  deps: AssetStoreDeps,
): Promise<Response> {
  if (!deps.store) return notFound(deps);

  const cached = await deps.cache.match(request);
  if (cached) return cached;

  let hit:
    | { pathname: string; object: AssetObject & { body: ReadableStream } }
    | undefined;
  for (const pathname of storedPathnames(url.pathname)) {
    const object = await deps.store.get(`${deps.assetPrefix}${pathname}`);
    if (object?.body) {
      hit = { pathname, object: { ...object, body: object.body } };
      break;
    }
  }
  if (!hit) return notFound(deps);

  const { object } = hit;
  const headers = new Headers({
    // Inferred from the name the object is STORED under, never the request's:
    // the request names a route, and only the stored name says what the bytes
    // are.
    "content-type": object.httpMetadata?.contentType || contentTypeFor(hit.pathname),
    "cache-control": IMMUTABLE_CACHE_CONTROL,
  });
  if (object.httpEtag) headers.set("etag", object.httpEtag);

  const response = new Response(object.body, { status: 200, headers });
  deps.waitUntil(deps.cache.put(request, response.clone()));
  return response;
}
