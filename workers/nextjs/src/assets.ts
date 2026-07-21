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

function notFound(): Response {
  return new Response("Not Found", { status: 404 });
}

// serveStaticAsset answers a static-asset request from the R2 cache store,
// fronted by the colo Cache API so a hot asset costs no R2 read at all. Every
// object this build could ever serve was written once, at its own
// build-id-scoped path — nothing at that key is ever overwritten — so a colo
// hit never needs revalidation: the response is cached with immutable
// headers. Always returns a Response (never throws): a miss, or no store
// bound at all, is a plain 404.
export async function serveStaticAsset(
  request: Request,
  url: URL,
  deps: AssetStoreDeps,
): Promise<Response> {
  if (!deps.store) return notFound();

  const cached = await deps.cache.match(request);
  if (cached) return cached;

  const key = `${deps.assetPrefix}${url.pathname}`;
  const object = await deps.store.get(key);
  if (!object?.body) return notFound();

  const headers = new Headers({
    "content-type": object.httpMetadata?.contentType || contentTypeFor(url.pathname),
    "cache-control": IMMUTABLE_CACHE_CONTROL,
  });
  if (object.httpEtag) headers.set("etag", object.httpEtag);

  const response = new Response(object.body, { status: 200, headers });
  deps.waitUntil(deps.cache.put(request, response.clone()));
  return response;
}
