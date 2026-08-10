// Next owns trailing slashes in three separate places, all driven by one config
// value, and a pathname has to pass through all three before it means anything:
//
//   1. the internal redirects Next unshifts ahead of every other route
//      (load-custom-routes.ts) decide the *canonical* form the client must be on;
//   2. the filesystem lookup drops one trailing slash unconditionally
//      (filesystem.ts) to get the *routing* form the build's pathnames are keyed by;
//   3. a data request is rewritten to its page path, with the slash re-added,
//      before middleware sees it (resolve-routes.ts).
//
// These three functions are those three sites, and nothing downstream of `serve`
// may reimplement any of them.
//
// A locale prefix is not a form this module knows: pathnames arrive with whatever
// locale segment they were requested under, which is all the worker's routing
// deals in (it passes i18n: undefined to resolveRoutes).

export interface TrailingSlashConfig {
  basePath: string;
  trailingSlash?: boolean;
  skipTrailingSlashRedirect?: boolean;
  skipMiddlewareUrlNormalize?: boolean;
}

// Next matches its internal redirects against the path with basePath removed,
// and re-adds basePath to the destination — so a path that is not under basePath
// cannot match them at all, and undefined is what says so. The boundary is a
// segment, not a prefix: /docsy is not under /docs.
export function withoutBasePath(
  pathname: string,
  basePath: string,
): string | undefined {
  if (!basePath) return pathname;
  if (pathname === basePath) return "";
  if (pathname.startsWith(`${basePath}/`)) return pathname.slice(basePath.length);
  return undefined;
}

// `/:file(...(?:[^/]+/)*[^/]+\.\w+)/` — the strip rule's final segment.
const DOTTED_SEGMENT = /\.\w+$/;

// `(?!\.well-known(?:/.*)?)` — the lookahead both trailingSlash: true rules carry.
function isWellKnown(rest: string): boolean {
  return rest.startsWith("/.well-known");
}

function finalSegment(rest: string): string {
  return rest.split("/").pop() ?? "";
}

// The trailingSlash policy itself, with no regard for skipTrailingSlashRedirect:
// what Next's internal redirects would rewrite this pathname to.
function applyPolicy(
  pathname: string,
  config: TrailingSlashConfig,
  isDataRequest: boolean,
): string {
  const basePath = config.basePath ?? "";
  const rest = withoutBasePath(pathname, basePath);
  if (rest === undefined) return pathname;

  if (!config.trailingSlash) {
    // `/:path+/` — generic, and deliberately not scoped to dotted segments or
    // .well-known the way the trailingSlash: true rules are. That asymmetry is
    // Next's own.
    const stripped = rest.endsWith("/") ? rest.slice(0, -1) : rest;
    return `${basePath}${stripped}` || "/";
  }

  if (isWellKnown(rest)) return pathname;

  if (rest.endsWith("/")) {
    const dotted = DOTTED_SEGMENT.test(finalSegment(rest.slice(0, -1)));
    if (!dotted || isDataRequest) return pathname;
    return `${basePath}${rest.slice(0, -1)}` || "/";
  }

  // rest === "" is the bare basePath, which Next gives its own redirect rule to
  // exactly this effect.
  if (finalSegment(rest).includes(".")) return pathname;
  return `${basePath}${rest}/`;
}

// The form the client must be on — what a 308 would point at. Equal to the input
// when the request is already canonical.
export function canonicalPathname(
  pathname: string,
  config: TrailingSlashConfig,
  isDataRequest = false,
): string {
  if (config.skipTrailingSlashRedirect) return pathname;
  return applyPolicy(pathname, config, isDataRequest);
}

// The form everything below routing is keyed by: the build's pathnames, the R2
// asset key, the colo cache key, the origin forward. Config-independent and
// unconditional, exactly as Next's filesystem lookup is — suppressing it under
// skipTrailingSlashRedirect would leave every canonical `/a/` a 404.
export function routingPathname(pathname: string): string {
  if (pathname === "/" || !pathname.endsWith("/")) return pathname;
  return pathname.slice(0, -1);
}

// `${basePath}/_next/data/${buildId}/<page>.json` -> `<page>`, or undefined when
// this is not a data pathname.
function dataPagePathname(
  pathname: string,
  config: TrailingSlashConfig,
  buildId: string,
): string | undefined {
  const basePath = config.basePath ?? "";
  const prefix = `${basePath}/_next/data/${buildId}/`;
  if (!pathname.startsWith(prefix) || !pathname.endsWith(".json")) return undefined;
  const page = pathname.slice(prefix.length, -".json".length);
  return `${basePath}${page === "index" ? "" : `/${page}`}` || "/";
}

// Whether a pathname is a genuine `/_next/data/<buildId>/….json` request — the
// URL-derived source of truth for "is this a data request", used wherever that
// question used to be answered by trusting a client-supplied x-nextjs-data
// header. A client cannot make this true by sending a header, and Next itself
// never treats one as a data request unless the URL says so either.
export function isNextDataPathname(
  pathname: string,
  config: TrailingSlashConfig,
  buildId: string,
): boolean {
  return dataPagePathname(pathname, config, buildId) !== undefined;
}

// The URL middleware is handed, which is not the routing URL: middleware runs
// ahead of the filesystem lookup, so it sees the canonical form, and it sees a
// data request as the page that request is for.
//
// Takes the pathname as requested, which is already canonical — anything else
// was redirected before middleware could run — and is the only form left when
// skipTrailingSlashRedirect or skipMiddlewareUrlNormalize makes the canonical
// form unrecoverable. Idempotent on a canonical input, so a routing-form
// pathname is also a valid argument wherever the two coincide.
//
// This is the form the middleware *matchers* are tested against: it matches on
// its own parsedUrl.pathname (resolve-routes.ts's `middleware` route), after
// middleware_next_data has already rewritten a data path to its page there. That
// rewrite is unconditional, but the trailing-slash re-add it then applies
// (maybeAddTrailingSlash) is gated on !skipProxyUrlNormalize, same as
// skipMiddlewareUrlNormalize below.
export function middlewareMatchPathname(
  pathname: string,
  config: TrailingSlashConfig,
  buildId: string,
): string {
  const page = dataPagePathname(pathname, config, buildId);
  if (page !== undefined) {
    const slash = config.trailingSlash && !config.skipMiddlewareUrlNormalize;
    return slash && page !== "/" ? `${page}/` : page;
  }
  return canonicalPathname(pathname, config, false);
}

// The URL middleware is handed. The same form, except that
// skipMiddlewareUrlNormalize (skipProxyUrlNormalize) suppresses every one of
// those normalizations at once — it does not suppress them one by one, it makes
// Next hand middleware `initURL`, the URL as the client sent it, in place of the
// routed one (next-server.ts's runMiddleware: `if
// (this.nextConfig.skipProxyUrlNormalize) url = getRequestMeta(req, 'initURL')`).
// The router still rewrites its own parsedUrl for the filesystem lookup and the
// matchers above, but middleware never sees that URL, so the data-to-page
// rewrite, the locale prefix and the slash re-add all stay off it. The
// bundle-side __NEXT_NO_MIDDLEWARE_URL_NORMALIZE then keeps NextURL from
// re-deriving them (next-url.ts's parseData) while still reading the locale out
// of the data pathname (get-next-pathname-info.ts).
export function middlewarePathname(
  pathname: string,
  config: TrailingSlashConfig,
  buildId: string,
): string {
  if (config.skipMiddlewareUrlNormalize) return pathname;
  return middlewareMatchPathname(pathname, config, buildId);
}
