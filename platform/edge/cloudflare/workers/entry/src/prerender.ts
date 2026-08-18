import { refreshHeader } from "@framework/next-cache";
import {
  ENTRY_HEADER,
  isOnDemandRevalidate,
  isRscRequest,
  shouldBypass,
  type PrerenderContext,
  type Prerenderer,
} from "@framework/next-router";
import { headResponse, withStatus } from "@framework/next-router/http-cache";
import { asSegmentPayload, isSegmentPrefetch } from "@framework/next-router/segment";

import {
  admitRefresh,
  cacheKey,
  hasDraftCookie,
  refreshOutcome,
  serveCached,
  servedFromStore,
  storeInColo,
  SUPPRESS_SELF_REVALIDATION,
  type CacheDeps,
  type CacheTarget,
} from "./cache";
import { intercept, type InterceptDeps, type InterceptionConfig } from "./interception";
import { composePpr, resumeRequest } from "./ppr";
import { enqueued, type RevalidationRoute } from "./revalidation";
import { createTagClock, type TagClock } from "./tag-clock";

const RSC_FORWARD_HEADERS = new Set([
  "rsc",
  "next-router-prefetch",
  "next-router-state-tree",
  "next-router-segment-prefetch",
  "next-url",
]);

const PREFETCH_PURPOSE = "purpose";

export type InterceptionTier = Pick<
  InterceptDeps,
  "store" | "snapshotCache" | "now" | "waitUntil"
> & { config: InterceptionConfig };

export interface PrerenderTierDeps {
  cache: CacheDeps;
  interception?: InterceptionTier;
  scope: string;
  basePath: string;
}

export function coloPrerender(deps: PrerenderTierDeps): Prerenderer {
  return (context) => prerender(context, deps);
}

async function prerender(
  ctx: PrerenderContext,
  deps: PrerenderTierDeps,
): Promise<Response> {
  const { request, url, target, headers, render, forward, forwardUrl } = ctx;
  const cache = deps.cache;
  const edgeEntryKey = target.edgeEntryKey;

  const segment = isSegmentPrefetch(request.headers);
  const answer = segment
    ? async (rendered: Request) => asSegmentPayload(await render(rendered))
    : render;

  const personalized =
    hasDraftCookie(request) || headers.has("x-middleware-set-cookie");
  const onDemand =
    isOnDemandRevalidate(request, target.config) &&
    (request.method === "GET" || request.method === "HEAD") &&
    !personalized;

  if (
    !onDemand &&
    (shouldBypass(request, url, target.config) ||
      request.method !== "GET" ||
      personalized)
  ) {
    const response = await answer(forward(request, headers));
    return withStatus(response, "BYPASS");
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

  const isNextData =
    url.pathname.startsWith(deps.basePath + "/_next/data/") &&
    url.pathname.endsWith(".json");

  const admissionTier =
    deps.interception && !isNextData ? deps.interception : undefined;

  const suppressed = SUPPRESS_SELF_REVALIDATION && admissionTier !== undefined;

  const originHeaders = new Headers(safeHeaders);
  if (suppressed) {
    originHeaders.set(PREFETCH_PURPOSE, "prefetch");
  }
  const origin = () => answer(forward(request, originHeaders));

  const plainHeaders = new Headers(safeHeaders);
  plainHeaders.delete(PREFETCH_PURPOSE);
  const originBlocking = (refreshing: number) => {
    const refreshHeaders = new Headers(plainHeaders);
    refreshHeaders.set(refreshHeader, String(refreshing));
    return answer(forward(request, refreshHeaders));
  };

  const revalidates = !edgeEntryKey;

  const routePath = ctx.routePath;
  const keyResult = cacheKey(
    deps.scope,
    url.pathname,
    url,
    request.headers,
    target.config.renderingMode,
    target.allowQuery,
  );
  const refreshKey = `${deps.scope}:${routePath}`;

  const publicUrl = new URL(request.url);
  const revalidation: RevalidationRoute | undefined =
    admissionTier && revalidates && target.id !== undefined && routePath.startsWith("/")
      ? {
          headers: {
            ...(target.entryKey !== undefined
              ? { [ENTRY_HEADER]: target.entryKey }
              : {}),
            "x-forwarded-host": publicUrl.host,
            "x-forwarded-proto": publicUrl.protocol.replace(/:$/, ""),
          },
          expect: null,
          isrPrefix: admissionTier.config.isrPrefix,
          routeId: target.id,
          routePath,
        }
      : undefined;

  const cacheTarget: CacheTarget = {
    key: keyResult.cacheable ? keyResult.key : "",
    refreshKey,
    revalidation,
    segment,
    suppressed,
    tags: target.tags,
    revalidate:
      typeof target.fallback?.initialRevalidate === "number"
        ? target.fallback.initialRevalidate
        : undefined,
    expiration: target.fallback?.initialExpiration,
  };

  if (onDemand) {
    const onDemandHeaders = new Headers(plainHeaders);
    onDemandHeaders.set("x-prerender-revalidate", target.config.bypassToken ?? "");
    const rendered = await render(
      forward(new Request(request.url, { method: "GET" }), onDemandHeaders),
    );
    if (keyResult.cacheable) {
      await storeInColo(cacheTarget, cache, rendered.clone());
    }
    const answered = withStatus(rendered, "BYPASS");
    return request.method === "HEAD" ? headResponse(answered) : answered;
  }

  let cachingOrigin = origin;
  let tagClock: TagClock | undefined;
  let cacheDeps = cache;
  if (admissionTier) {
    const { config, ...interceptDeps } = admissionTier;
    tagClock = createTagClock(config, interceptDeps);
    const interceptTarget = {
      routePath,
      fallbackPath: ctx.resolvedPathname,
      revalidate: target.fallback?.initialRevalidate,
      expiration: target.fallback?.initialExpiration,
      tags: target.tags,
    };
    const read = once(() =>
      intercept(request, interceptTarget, config, {
        ...interceptDeps,
        tagClock,
      }),
    );

    const satisfiedFromBelow = async (refreshing: number) => {
      const below = await intercept(request, interceptTarget, config, {
        ...interceptDeps,
        tagClock,
        freshRead: true,
      });
      if (!below) return false;
      const answered = below.kind === "complete" ? below.response : below.shell;
      if (below.lastModified <= refreshing) {
        answered.body?.cancel();
        return false;
      }
      if (!keyResult.cacheable) {
        answered.body?.cancel();
        return true;
      }
      if (below.kind !== "complete") {
        answered.body?.cancel();
        return false;
      }
      await storeInColo(cacheTarget, cache, below.response);
      return true;
    };
    cacheDeps = { ...cache, satisfiedFromBelow };

    const mayPostpone =
      target.config.renderingMode !== "STATIC" &&
      request.method === "GET" &&
      !hasDraftCookie(request) &&
      !isRscRequest(request.headers);

    if (mayPostpone) {
      const hit = await read();
      if (hit?.kind === "ppr") {
        if (hit.stale && revalidates) {
          admitRefresh(
            cacheDeps,
            refreshKey,
            hit.lastModified,
            async () => {
              if (await enqueued(cacheDeps.enqueueRevalidation, revalidation, hit.lastModified)) {
                return "landed";
              }
              const response = await originBlocking(hit.lastModified);
              response.body?.cancel();
              return refreshOutcome(response);
            },
            hit.staleForMs,
          );
        }
        return composePpr(
          hit,
          render(
            resumeRequest(
              forwardUrl,
              request,
              hit.postponed,
              target.pprChain?.headers,
            ),
          ),
        );
      }
    }

    cachingOrigin = async () => {
      const hit = await read();
      if (hit?.kind !== "complete") return origin();
      if (hit.stale && revalidates) {
        admitRefresh(
          cacheDeps,
          refreshKey,
          hit.lastModified,
          async () => {
            if (await enqueued(cacheDeps.enqueueRevalidation, revalidation, hit.lastModified)) {
              return "landed";
            }
            const response = await originBlocking(hit.lastModified);
            const outcome = refreshOutcome(response);
            await storeInColo(cacheTarget, cache, response);
            return outcome;
          },
          hit.staleForMs,
        );
      }
      return servedFromStore(hit.response, hit.stale);
    };
  }

  if (!keyResult.cacheable) {
    const uncacheableHeaders = new Headers(headers);
    if (suppressed) {
      uncacheableHeaders.set(PREFETCH_PURPOSE, "prefetch");
    }
    const response = await answer(forward(request, uncacheableHeaders));
    return withStatus(response, "MISS");
  }

  return serveCached(
    request,
    cacheTarget,
    cacheDeps,
    cachingOrigin,
    originBlocking,
    tagClock,
  );
}

function once<T>(run: () => Promise<T>): () => Promise<T> {
  let pending: Promise<T> | undefined;
  return () => (pending ??= run());
}
