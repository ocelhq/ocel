import type { DispatchTarget, NextRouteTable } from "@framework/next-protocol/route-table";
import type { BuildContext, BuildPlan } from "./build-plan.mjs";
import { cacheTags } from "./cache-seed.mjs";
import { edgeEntryOf } from "./edge-sources.mjs";
import type { FunctionBundles } from "./function-bundles.mjs";
import { imageConfigHash } from "./image-config.mjs";
import { middlewareEntryKey } from "./launcher.mjs";
import { isDocumentRouteKind, routeKeyOf, staticRouteKeyOf } from "./route-keys.mjs";
import type { StaticAssets } from "./static-assets.mjs";

const exactly =
  <T,>() =>
  <U extends T & Record<Exclude<keyof U, keyof T>, never>>(value: U): U =>
    value;

type Dispatch = Record<string, DispatchTarget>;

interface RouteDispatch {
  key: string;
  original: string;
  value: DispatchTarget;
}

export function warnOriginMiddlewareBlastRadius(
  ctx: BuildContext,
  plan: BuildPlan,
  statics: StaticAssets,
): void {
  const { originMiddleware } = plan;
  if (!originMiddleware) return;
  const cachedPathnames = [
    ...ctx.outputs.prerenders.map((p) => p.pathname),
    ...ctx.outputs.staticFiles.map((f) => f.pathname),
    ...statics.publicFiles.map((f) => f.pathname),
  ];
  const matchers = originMiddleware.config.matchers ?? [];
  const regexes = matchers.map((m) => new RegExp(m.sourceRegex));
  const matches = (pathname: string) =>
    matchers.length === 0 || regexes.some((re) => re.test(pathname));
  const hit = [...new Set(cachedPathnames.filter(matches))].sort();
  if (hit.length === 0) return;

  const sample = hit.slice(0, 5);
  console.warn(
    `ocel: the origin middleware's matcher covers ${hit.length} cached route(s), including ${sample.join(", ")}${hit.length > sample.length ? ", …" : ""} — each will round-trip to the function region on every request instead of being answered at the edge`,
  );
}

function routeDispatchOf(
  ctx: BuildContext,
  plan: BuildPlan,
  functions: FunctionBundles,
): RouteDispatch[] {
  const { basePath, pagesDistDir, routeKinds } = plan;

  const functionDispatch = [...plan.functionRoutes, ...plan.originEdgeRoutes].map((o) => {
    const routeKey = routeKeyOf(o, basePath);
    const entryKey = functions.entryKeyByPathname.get(routeKey);
    if (entryKey === undefined) {
      throw new Error(
        `ocel: node route "${routeKey}" resolves to no bundle entry — this build cannot be served`,
      );
    }
    return {
      key: routeKey,
      original: o.pathname,
      value: {
        kind: "function" as const,
        id: functions.bundleNameOf(entryKey, routeKey),
        entryKey,
        ...(isDocumentRouteKind(routeKinds.get(o.id) ?? o.type) && { page: true }),
      },
    };
  });

  const edgeDispatch = plan.hostedEdgeRoutes.map((o) => ({
    key: routeKeyOf(o, basePath),
    original: o.pathname,
    value: { kind: "edge" as const, entryKey: edgeEntryOf(o).entryKey },
  }));

  const staticDispatch = ctx.outputs.staticFiles.map((o) => ({
    key: staticRouteKeyOf(o, pagesDistDir, basePath),
    original: o.pathname,
    value: { kind: "static" as const },
  }));

  const nextDataDispatch = plan.nextDataStaticFiles.map(({ pageKey, dataKey }) => ({
    key: dataKey,
    original: pageKey,
    value: { kind: "static" as const },
  }));

  return [...functionDispatch, ...edgeDispatch, ...staticDispatch, ...nextDataDispatch];
}

function refuseDispatchKeyCollisions(routeDispatch: readonly RouteDispatch[]): void {
  const dispatchKeyOrigin = new Map<string, string>();
  for (const { key, original } of routeDispatch) {
    const prior = dispatchKeyOrigin.get(key);
    if (prior !== undefined && prior !== original) {
      throw new Error(
        `ocel: routes "${prior}" and "${original}" both resolve to dispatch key "${key}" — this build cannot be served`,
      );
    }
    dispatchKeyOrigin.set(key, original);
  }
}

function prerenderDispatchOf(
  ctx: BuildContext,
  plan: BuildPlan,
  functions: FunctionBundles,
): [string, DispatchTarget][] {
  return ctx.outputs.prerenders.map((p) => {
    const routeKey = p.pathname;
    const allowQuery = p.config?.allowQuery;
    const tags = cacheTags(p, plan.programmableEdge);
    const entryKey = functions.entryKeyByRouteId.get(p.parentOutputId);
    const edgeEntryKey = functions.edgeEntryByOutputId.get(p.parentOutputId);
    if (entryKey === undefined && edgeEntryKey === undefined) {
      throw new Error(
        `ocel: prerender "${routeKey}" is parented by output "${p.parentOutputId}", which renders on neither a function nor the edge — nothing can regenerate it`,
      );
    }

    return [
      routeKey,
      {
        kind: "prerender",
        id: entryKey === undefined ? p.parentOutputId : functions.bundleNameOf(entryKey, routeKey),
        config: p.config as Extract<DispatchTarget, { kind: "prerender" }>["config"],
        fallback: {
          initialRevalidate: p.fallback?.initialRevalidate,
          initialExpiration: p.fallback?.initialExpiration,
        },
        ...(p.pprChain && { pprChain: p.pprChain }),
        ...(tags.length > 0 && { tags }),
        ...(allowQuery && { allowQuery }),
        ...(entryKey !== undefined && { entryKey }),
        ...(edgeEntryKey !== undefined && { edgeEntryKey }),
      },
    ];
  });
}

function buildDispatch(
  ctx: BuildContext,
  plan: BuildPlan,
  functions: FunctionBundles,
  statics: StaticAssets,
): Dispatch {
  const routeDispatch = routeDispatchOf(ctx, plan, functions);
  refuseDispatchKeyCollisions(routeDispatch);
  return Object.fromEntries([
    ...routeDispatch.map((d) => [d.key, d.value] as const),
    ...statics.publicFiles.map((p) => [p.pathname, { kind: "static" as const }] as const),
    ...prerenderDispatchOf(ctx, plan, functions),
  ]);
}

function errorRoutesOf(dispatch: Dispatch, basePath: string): NextRouteTable["errorRoutes"] {
  const isDispatchedErrorPage = (key: string): boolean => {
    const target = dispatch[key];
    if (!target) return false;
    if (target.kind === "function") return target.page === true;
    return target.kind === "prerender" || target.kind === "static";
  };
  const notFound = [`${basePath}/_not-found`, `${basePath}/404`].find(isDispatchedErrorPage);
  const serverError = [`${basePath}/500`].find(isDispatchedErrorPage);
  if (notFound === undefined && serverError === undefined) return undefined;
  return {
    ...(notFound !== undefined && { notFound }),
    ...(serverError !== undefined && { serverError }),
  };
}

function middlewareOf(plan: BuildPlan, functions: FunctionBundles): NextRouteTable["middleware"] {
  const { middleware, originMiddleware } = plan;
  if (!middleware) return undefined;
  return originMiddleware
    ? {
        runtime: "nodejs",
        id: functions.seededBundleId!,
        entryKey: middlewareEntryKey,
        matchers: originMiddleware.config.matchers ?? [],
      }
    : {
        runtime: "edge",
        entryKey: edgeEntryOf(middleware).entryKey,
        matchers: middleware.config.matchers ?? [],
      };
}

function assetPrefixField(
  assetPrefix: string | undefined,
  basePath: string,
): Partial<Pick<NextRouteTable, "assetPrefixPathname">> {
  if (!assetPrefix) return {};
  const pathname =
    /https?:\/\//.test(assetPrefix) && URL.canParse(assetPrefix)
      ? new URL(assetPrefix).pathname
      : assetPrefix;
  if (!pathname || pathname === "/") return {};
  const assetPrefixPathname = pathname.startsWith("/") ? pathname : `/${pathname}`;
  return assetPrefixPathname === basePath ? {} : { assetPrefixPathname };
}

export function buildRouteTable(
  ctx: BuildContext,
  plan: BuildPlan,
  functions: FunctionBundles,
  statics: StaticAssets,
  appName: string,
): NextRouteTable {
  const { routing, outputs, config, buildId } = ctx;
  const { basePath, pagesDistDir, images } = plan;
  const dispatch = buildDispatch(ctx, plan, functions, statics);
  const errorRoutes = errorRoutesOf(dispatch, basePath);
  const middleware = middlewareOf(plan, functions);

  return exactly<NextRouteTable>()({
    rootFunction: functions.rootFunction,
    buildId,
    appName,
    basePath: config.basePath || "",
    ...assetPrefixField(config.assetPrefix, basePath),
    trailingSlash: !!config.trailingSlash,
    skipTrailingSlashRedirect: !!config.skipTrailingSlashRedirect,
    skipMiddlewareUrlNormalize: !!config.skipMiddlewareUrlNormalize,
    i18n: (config.i18n ?? undefined) as NextRouteTable["i18n"],
    ...(images && { images: { ...images, configHash: imageConfigHash(images) } }),
    assetHashes: statics.assetHashes,
    pathnames: [
      ...new Set([
        ...plan.allRoutes.map((o) => routeKeyOf(o, basePath)),
        ...outputs.prerenders.map((p) => p.pathname),
        ...outputs.staticFiles.map((o) => staticRouteKeyOf(o, pagesDistDir, basePath)),
        ...statics.publicFiles.map((p) => p.pathname),
        ...plan.nextDataStaticFiles.map((d) => d.dataKey),
      ]),
    ],
    routes: routing,
    ...(middleware && { middleware }),
    ...(errorRoutes && { errorRoutes }),
    dispatch,
  });
}
