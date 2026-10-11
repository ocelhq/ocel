import {
  ENTRY_HEADER,
  isOnDemandRevalidate,
  type PrerenderContext,
  type Prerenderer,
  shouldBypass,
} from "@framework/next-router";
import {
  type CacheStatus,
  headResponse,
  NEXT_CACHE_STATUS,
  withStatus,
} from "@framework/next-router/http-cache";
import { asSegmentPayload, isSegmentPrefetch } from "@framework/next-router/segment";
import { edgeCacheHeader, pprShellHeader } from "@platform/edge-contract/edge-cache";
import { dropEmptyBodySentinel } from "@platform/edge-contract/empty-body";
import type { PurgeGroup } from "@platform/edge-contract/purge";
import { buildResumeRequest, composePpr, PPR_SHELL_VARIANT, readPostponedShell } from "./ppr";
import { buildPathTag, ORIGIN_VARY } from "./serve-headers";
import {
  buildCanonicalUrl,
  buildEdgeCall,
  buildOriginCall,
  buildServeRequest,
  buildVariantPath,
  RENDER_RELEASE_HEADER,
  type RenderRelease,
} from "./serve-key";
import type { ServeFactory } from "./serve-objects";

const RSC_FORWARD_HEADERS = new Set([
  "rsc",
  "next-router-prefetch",
  "next-router-state-tree",
  "next-router-segment-prefetch",
  "next-url",
]);

const DRAFT_COOKIE = "__prerender_bypass";

export interface Serving {
  serve: ServeFactory;
  host: string;
  release: string;
  slug: string;
  isrPrefix: string;
  purge(groups: PurgeGroup[], awaited: boolean): Promise<void>;
}

export interface ServePrerenderDeps {
  serving: Serving;
  app: string;
}

export function hasDraftCookie(request: Request): boolean {
  const cookie = request.headers.get("cookie");
  return cookie !== null && new RegExp(`(?:^|;\\s*)${DRAFT_COOKIE}=`).test(cookie);
}

export function servePrerender(deps: ServePrerenderDeps): Prerenderer {
  return (context) => prerender(context, deps);
}

function readTier(response: Response): CacheStatus {
  switch (response.headers.get("cf-cache-status")?.toUpperCase()) {
    case "HIT":
    case "REVALIDATED":
      return "HIT";
    case "STALE":
    case "UPDATING":
      return "STALE";
    default:
      return "MISS";
  }
}

function buildVisitorResponse(response: Response, segment: boolean): Response {
  const tier = readTier(response);
  const headers = new Headers(response.headers);
  for (const name of ["cache-tag", "cloudflare-cdn-cache-control", "cf-cache-status"]) {
    headers.delete(name);
  }
  const vary = headers.get(ORIGIN_VARY);
  headers.delete(ORIGIN_VARY);
  if (vary !== null) headers.set("vary", vary);
  if (headers.has(NEXT_CACHE_STATUS) && tier !== "MISS") headers.set(NEXT_CACHE_STATUS, tier);
  const restored = dropEmptyBodySentinel(
    new Response(response.body, { status: response.status, headers }),
  );
  return withStatus(segment ? asSegmentPayload(restored) : restored, tier);
}

function isPprDocument(request: Request, target: PrerenderContext["target"]): boolean {
  return target.config.renderingMode === "PARTIALLY_STATIC" && !request.headers.has("rsc");
}

function findVariant(
  request: Request,
  url: URL,
  target: PrerenderContext["target"],
): string | null {
  if (isPprDocument(request, target)) return PPR_SHELL_VARIANT;
  const variant = buildVariantPath(url.pathname, request.headers, target.config.renderingMode);
  return variant === null ? null : variant.slice(url.pathname.length);
}

async function prerender(ctx: PrerenderContext, deps: ServePrerenderDeps): Promise<Response> {
  const { request, url, target, headers, render, forward } = ctx;
  const { serving } = deps;

  const answer = isSegmentPrefetch(request.headers)
    ? async (rendered: Request) => asSegmentPayload(await render(rendered))
    : render;
  const forwardUncached = (forwarded: Headers) => {
    const passed = new Headers(forwarded);
    passed.delete(edgeCacheHeader);
    return forward(request, passed);
  };

  const personalized = hasDraftCookie(request) || headers.has("x-middleware-set-cookie");
  const onDemand =
    isOnDemandRevalidate(request, target.config) &&
    (request.method === "GET" || request.method === "HEAD") &&
    !personalized;

  if (
    !onDemand &&
    (shouldBypass(request, url, target.config) || request.method !== "GET" || personalized)
  ) {
    return withStatus(await answer(forwardUncached(headers)), "BYPASS");
  }

  const safeHeaders = new Headers();
  const allowedHeaders = target.config.allowHeader?.map((h) => h.toLowerCase());
  for (const [name, value] of headers) {
    const lower = name.toLowerCase();
    if (
      allowedHeaders?.includes(lower) ||
      RSC_FORWARD_HEADERS.has(lower) ||
      ctx.overriddenHeaders.has(lower)
    ) {
      safeHeaders.set(name, value);
    }
  }

  if (onDemand) {
    const onDemandHeaders = new Headers(safeHeaders);
    onDemandHeaders.set("x-prerender-revalidate", target.config.bypassToken ?? "");
    const rendered = await render(
      forward(new Request(request.url, { method: "GET" }), onDemandHeaders),
    );
    if (rendered.ok) {
      const tag = buildPathTag(serving.release, ctx.forwardUrl.pathname);
      await serving.purge([{ mode: "purge", tags: [tag] }], true);
    }
    const answered = withStatus(rendered, "BYPASS");
    return request.method === "HEAD" ? headResponse(answered) : answered;
  }

  const variant = findVariant(request, url, target);
  if (variant === null) {
    return withStatus(await answer(forwardUncached(headers)), "MISS");
  }

  if (target.entryKey !== undefined) safeHeaders.set(ENTRY_HEADER, target.entryKey);
  safeHeaders.set(edgeCacheHeader, "1");
  const pprDocument = isPprDocument(request, target);
  if (pprDocument) safeHeaders.set(pprShellHeader, "1");
  const forwarded = forward(request, safeHeaders);
  const canonicalUrl = buildCanonicalUrl(new URL(forwarded.url), target.allowQuery);
  const coordinates = {
    host: serving.host,
    app: deps.app,
    release: serving.release,
    variant: variant || undefined,
  };
  const { url: canonical, props } = target.edgeEntryKey
    ? buildEdgeCall(canonicalUrl, coordinates, target.edgeEntryKey)
    : buildOriginCall(canonicalUrl, coordinates);
  const served = buildServeRequest(canonical, forwarded);
  if (target.edgeEntryKey) {
    const release: RenderRelease = {
      slug: serving.slug,
      isrPrefix: serving.isrPrefix,
      tags: target.tags ?? [],
    };
    served.headers.set(RENDER_RELEASE_HEADER, JSON.stringify(release));
  }
  const response = await serving.serve({ props }).fetch(served);
  const visitor = buildVisitorResponse(response, isSegmentPrefetch(request.headers));
  if (!pprDocument) return visitor;
  const postponed = await readPostponedShell(visitor);
  if (postponed === "unframed") return visitor;
  if (postponed === "malformed") return withStatus(await answer(forwardUncached(headers)), "MISS");
  return composePpr(
    postponed.shell,
    render(
      buildResumeRequest(ctx.forwardUrl, request, postponed.postponed, target.pprChain?.headers),
    ),
  );
}
