import { boundCacheTags } from "@framework/next-cache/cache-tags";
import { cacheKey, variantHeadersFile } from "@framework/next-cache/naming";
import type { RoutingManifest } from "@framework/next-protocol/routing-manifest";
import type { ServeDescriptor } from "@platform/edge-contract/serve";
import type { AdapterOutput, NextAdapter } from "next";
import { PHASE_PRODUCTION_BUILD } from "next/constants.js";
import { createHash } from "node:crypto";
import { createReadStream, createWriteStream, writeFileSync } from "node:fs";
import {
  copyFile,
  cp,
  lstat,
  mkdir,
  readFile,
  readdir,
  readlink,
  rm,
  stat,
  symlink,
  writeFile,
} from "node:fs/promises";
import { basename, dirname, join, relative, sep } from "node:path";
import { pipeline } from "node:stream/promises";
import {
  compileImageConfig,
  imageConfigHash,
  serializeImageConfig,
} from "./image-config.mjs";
import { packBundles, type PackedMember } from "./pack.mjs";
import { stableStringify } from "./stable-json.mjs";

const exactly =
  <T,>() =>
  <U extends T & Record<Exclude<keyof U, keyof T>, never>>(value: U): U =>
    value;

const launcherName = "__next_launcher.cjs";
const dispatchName = "__ocel_dispatch.cjs";

export const middlewareEntryKey = "/_middleware";

const programmableEdgeKind = "cloudflare";

function isProgrammableEdge(kind: string | undefined): boolean {
  return kind === programmableEdgeKind;
}

function waivedNeeds(declared: string | undefined): Set<string> {
  return new Set(
    (declared ?? "")
      .split(",")
      .map((name) => name.trim())
      .filter(Boolean),
  );
}

function resolveOutputRoot(): string {
  return process.env.OCEL_OUTPUT_DIR || join(process.cwd(), ".ocel/output");
}

const cacheHandlerPath = "/opt/ocel/next/cache-handler.cjs";
const useCacheHandlerPaths = {
  default: "/opt/ocel/next/use-cache-default.cjs",
  remote: "/opt/ocel/next/use-cache-remote.cjs",
};

async function installCacheHandler(): Promise<string> {
  const dest = join(process.cwd(), ".ocel", "cache-handler.cjs");
  await mkdir(dirname(dest), { recursive: true });
  await copyFile(new URL("edge-cache-handler.cjs", import.meta.url), dest);
  return dest;
}

const adapter = {
  name: "ocel-adapter",

  async modifyConfig(config, { phase }) {
    if (phase === PHASE_PRODUCTION_BUILD) {
      return {
        ...config,
        cacheMaxMemorySize: 0,
        cacheHandler: await installCacheHandler(),
        experimental: { ...config.experimental, trustHostHeader: true },
      };
    }
    return config;
  },

  async onBuildComplete(args) {
    const {
      routing,
      outputs,
      projectDir,
      repoRoot,
      distDir,
      config,
      nextVersion,
      buildId,
    } = args;

    const basePath = config.basePath || "";

    const allRoutes = [
      ...outputs.pages,
      ...outputs.pagesApi,
      ...outputs.appPages,
      ...outputs.appRoutes,
    ];

    const pagesDistDir = join(distDir, "server", "pages") + sep;

    const { middleware } = outputs;
    const nodeMiddleware = middleware?.runtime === "nodejs" ? middleware : undefined;
    const programmableEdge = isProgrammableEdge(process.env.OCEL_EDGE_KIND);
    const waived = waivedNeeds(process.env.OCEL_ALLOW_DEGRADED);
    const compileEdgeOnOrigin = (need: string) =>
      !programmableEdge && waived.has(need);
    const edgeMiddleware = middleware?.runtime === "edge" ? middleware : undefined;
    const originEdgeMiddleware = compileEdgeOnOrigin("edge-middleware")
      ? edgeMiddleware
      : undefined;
    const originMiddleware = nodeMiddleware ?? originEdgeMiddleware;

    const nextDataStaticFiles = middleware
      ? outputs.staticFiles
          .filter((f) => f.filePath.startsWith(pagesDistDir))
          .map((f) => {
            const pageKey = staticRouteKeyOf(f, pagesDistDir, basePath);
            return {
              pageKey,
              dataKey: nextDataPathnameOf(pageKey, buildId, basePath),
            };
          })
      : [];

    const images = compileImageConfig(config.images);

    const functionRoutes = allRoutes.filter((r) => r.runtime === "nodejs");
    const edgeRoutes = allRoutes.filter((r) => r.runtime === "edge");
    const originEdgeRoutes = compileEdgeOnOrigin("edge-runtime") ? edgeRoutes : [];
    const hostedEdgeRoutes = compileEdgeOnOrigin("edge-runtime") ? [] : edgeRoutes;

    const edgeEntryByOutputId = new Map(
      hostedEdgeRoutes.map((r) => [r.id, edgeEntryOf(r).entryKey]),
    );
    const edgeRouteIds = new Set(edgeRoutes.map((r) => r.id));
    const inertPrerenders = outputs.prerenders
      .filter((p) => edgeRouteIds.has(p.parentOutputId))
      .map((p) => p.pathname);
    if (inertPrerenders.length > 0) {
      console.warn(
        `ocel: revalidate is inert for edge-rendered route(s) ${inertPrerenders.join(", ")} — edge ISR is not supported yet, and an edge route compiled into the origin function bypasses the cache handler just the same`,
      );
    }

    const outputRoot = resolveOutputRoot();
    const appRel = relative(repoRoot, projectDir);

    const appName = process.env.OCEL_APP_NAME || basename(projectDir);

    await patchCacheHandlers(distDir);

    const groups = new Map<string, typeof functionRoutes>();
    for (const route of functionRoutes) {
      const key = `${route.filePath}\0${JSON.stringify(route.config)}`;
      const members = groups.get(key);
      if (members) members.push(route);
      else groups.set(key, [route]);
    }

    const entryKeyByPathname = new Map<string, string>();
    const entryKeyByRouteId = new Map<string, string>();
    const entryRoutes: typeof functionRoutes = [];
    for (const members of groups.values()) {
      members.sort(
        (a, b) =>
          a.pathname.length - b.pathname.length ||
          (a.pathname < b.pathname ? -1 : 1),
      );
      const entry = members[0]!;
      entryRoutes.push(entry);
      for (const m of members) {
        entryKeyByPathname.set(routeKeyOf(m, basePath), entry.id);
        entryKeyByRouteId.set(m.id, entry.id);
      }
    }

    const assetsOf = (route: { assets: Record<string, string>; filePath: string }) => ({
      ...route.assets,
      [relative(repoRoot, route.filePath)]: route.filePath,
    });

    const assetSuffix = clientAssetSuffix(config);

    const originEdge = await planOriginEdgeEntries(
      distDir,
      [
        ...originEdgeRoutes,
        ...(originEdgeMiddleware ? [originEdgeMiddleware] : []),
      ],
      appRel,
      assetSuffix,
      (edgeEntryKey) =>
        originEdgeMiddleware &&
        edgeEntryKey === edgeEntryOf(originEdgeMiddleware).entryKey
          ? middlewareEntryKey
          : edgeEntryKey,
    );

    const seeded = nodeMiddleware !== undefined || originEdge !== null;
    const seedAssets = {
      ...(nodeMiddleware ? assetsOf(nodeMiddleware) : {}),
      ...(originEdge ? originEdge.seedAssets : {}),
    };

    const { bundles, missingAssets } = packBundles(entryRoutes, {
      entryKeyOf: (route) => route.id,
      assetsOf,
      partitionBy: configClass,
      ...(seeded && { seedAssets }),
    });
    const seededBundleId = seeded ? bundles[0]!.name : undefined;

    const bundleNameByEntryKey = new Map(
      bundles.flatMap((b) =>
        b.members.map((m) => [m.member.id, b.name] as const),
      ),
    );

    const bundleNameOf = (entryKey: string, pathname: string): string => {
      const name = bundleNameByEntryKey.get(entryKey);
      if (name === undefined) {
        throw new Error(
          `ocel: route "${pathname}" resolves to entry "${entryKey}", which no emitted bundle carries — this build cannot be served`,
        );
      }
      return name;
    };

    for (const route of originEdgeRoutes) {
      const entryKey = edgeEntryOf(route).entryKey;
      bundleNameByEntryKey.set(entryKey, seededBundleId!);
      entryKeyByPathname.set(routeKeyOf(route, basePath), entryKey);
      entryKeyByRouteId.set(route.id, entryKey);
    }

    const rootPathname = basePath || "/";
    const rootEntryKey = entryKeyByPathname.get(rootPathname);
    const entry =
      rootEntryKey === undefined ? "" : bundleNameOf(rootEntryKey, rootPathname);

    const routeKinds = routeKindsById(allRoutes, outputs.prerenders);

    const prerenderGroups = groupPrerenders(outputs.prerenders);

    const variantHeaders = JSON.stringify(
      variantHeaderProjection(prerenderGroups),
    );

    const routes = routeTable(entryKeyByPathname, routing.dynamicRoutes ?? []);

    const nextConfigProjection = {
      basePath: config.basePath || "",
      i18n: config.i18n ?? null,
      trailingSlash: !!config.trailingSlash,
      experimental: config.experimental ?? {},
      clientAssetSuffix: assetSuffix,
    };

    await Promise.all(
      bundles.map(async (bundle) => {
        const funcDir = join(outputRoot, "functions", `${bundle.name}.func`);

        for (const [destRel, srcAbs] of Object.entries(bundle.assets)) {
          await copyAsset(srcAbs, join(funcDir, destRel));
        }

        const dispatchDest = join(funcDir, appRel, dispatchName);
        await mkdir(dirname(dispatchDest), { recursive: true });
        await copyFile(
          new URL("next-dispatch.cjs", import.meta.url),
          dispatchDest,
        );

        const entries: Record<string, string> = Object.fromEntries(
          bundle.members.map(({ member }) => [
            member.id,
            "./" + relative(projectDir, member.filePath).split(sep).join("/"),
          ]),
        );
        if (nodeMiddleware && bundle.name === seededBundleId) {
          entries[middlewareEntryKey] =
            "./" +
            relative(projectDir, nodeMiddleware.filePath).split(sep).join("/");
        }
        if (originEdge && bundle.name === seededBundleId) {
          await copyFile(
            new URL("edge-node-entry.cjs", import.meta.url),
            join(funcDir, appRel, originEdgeRuntimeName),
          );
          for (const [name, body] of Object.entries(originEdge.files)) {
            const dest = join(funcDir, appRel, name);
            await mkdir(dirname(dest), { recursive: true });
            await writeFile(dest, body);
          }
          Object.assign(entries, originEdge.entries);
        }

        const launcherRel = join(appRel, launcherName);
        await writeFile(
          join(funcDir, launcherRel),
          renderLauncher(
            entries,
            primaryEntryKey(bundle.members),
            routes,
            nextConfigProjection,
          ),
        );

        await writeFile(
          join(funcDir, "config.json"),
          JSON.stringify({
            runtime: "nodejs24.x",
            handler: launcherRel,
            framework: "next",
            id: bundle.name,
            app: appName,
          }),
        );

        await writeFile(join(funcDir, variantHeadersFile), variantHeaders);
      }),
    );

    if (missingAssets.length > 0) {
      const sample = missingAssets.slice(0, 5);
      console.warn(
        `ocel: ${missingAssets.length} traced asset(s) have no source on disk and were not copied into the bundle — a route requiring one of them fails at runtime: ${sample.join(", ")}${missingAssets.length > sample.length ? ", …" : ""}`,
      );
    }

    const publicFiles = await collectPublicFiles(projectDir);
    const staticAssets = [
      ...publicFiles.map((f) => [f.pathname, f.filePath] as const),
      ...outputs.staticFiles.map((f) => [servedPathname(f), f.filePath] as const),
    ];
    const assetHashes: Record<string, string> = {};
    for (const [pathname, filePath] of staticAssets) {
      assetHashes[pathname] = await copyHashedFile(
        filePath,
        join(outputRoot, "static", pathname),
      );
    }
    for (const { dataKey } of nextDataStaticFiles) {
      assetHashes[dataKey] = await writeHashedFile(
        join(outputRoot, "static", dataKey),
        "{}",
      );
    }

    if (originMiddleware) {
      warnOriginMiddlewareBlastRadius(originMiddleware, [
        ...outputs.prerenders.map((p) => p.pathname),
        ...outputs.staticFiles.map((f) => f.pathname),
        ...publicFiles.map((f) => f.pathname),
      ]);
    }

    await emitCacheEntries(outputRoot, prerenderGroups, routeKinds);
    await emitFetchEntries(outputRoot, distDir);

    const functionDispatch = [...functionRoutes, ...originEdgeRoutes].map((o) => {
      const routeKey = routeKeyOf(o, basePath);
      const entryKey = entryKeyByPathname.get(routeKey);
      if (entryKey === undefined) {
        throw new Error(
          `ocel: node route "${routeKey}" resolves to no bundle entry — this build cannot be served`,
        );
      }
      return {
        key: routeKey,
        original: o.pathname,
        value: {
          kind: "lambda",
          id: bundleNameOf(entryKey, routeKey),
          entryKey,
          ...(isDocumentRouteKind(routeKinds.get(o.id) ?? o.type) && {
            page: true,
          }),
        },
      };
    });

    const edgeDispatch = hostedEdgeRoutes.map((o) => ({
      key: routeKeyOf(o, basePath),
      original: o.pathname,
      value: { kind: "edge", entryKey: edgeEntryOf(o).entryKey },
    }));

    const staticDispatch = outputs.staticFiles.map((o) => ({
      key: staticRouteKeyOf(o, pagesDistDir, basePath),
      original: o.pathname,
      value: { kind: "static" },
    }));

    const nextDataDispatch = nextDataStaticFiles.map(({ pageKey, dataKey }) => ({
      key: dataKey,
      original: pageKey,
      value: { kind: "static" },
    }));

    const routeDispatch = [
      ...functionDispatch,
      ...edgeDispatch,
      ...staticDispatch,
      ...nextDataDispatch,
    ];
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

    const publicDispatch = publicFiles.map((p) => [
      p.pathname,
      { kind: "static" },
    ]);

    const prerenderDispatch = outputs.prerenders.map((p) => {
      const routeKey = p.pathname;
      const allowQuery = p.config?.allowQuery;
      const tags = cacheTags(p);
      const entryKey = entryKeyByRouteId.get(p.parentOutputId);
      const edgeEntryKey = edgeEntryByOutputId.get(p.parentOutputId);
      if (entryKey === undefined && edgeEntryKey === undefined) {
        throw new Error(
          `ocel: prerender "${routeKey}" is parented by output "${p.parentOutputId}", which renders on neither a Lambda nor the edge — nothing can regenerate it`,
        );
      }

      return [
        routeKey,
        {
          kind: "prerender",
          id:
            entryKey === undefined
              ? p.parentOutputId
              : bundleNameOf(entryKey, routeKey),
          config: p.config,
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

    const dispatch = Object.fromEntries([
      ...routeDispatch.map((d) => [d.key, d.value]),
      ...publicDispatch,
      ...prerenderDispatch,
    ]);

    const isDispatchedErrorPage = (key: string): boolean => {
      const target = dispatch[key];
      if (!target) return false;
      if (target.kind === "lambda") return target.page === true;
      return target.kind === "prerender" || target.kind === "static";
    };
    const firstDispatchedErrorPage = (
      candidates: string[],
    ): string | undefined => candidates.find(isDispatchedErrorPage);
    const notFoundKey = firstDispatchedErrorPage([
      `${basePath}/404`,
      `${basePath}/_not-found`,
    ]);
    const notFoundFlightKey = firstDispatchedErrorPage([
      `${basePath}/_not-found`,
    ]);
    const serverErrorKey = firstDispatchedErrorPage([`${basePath}/500`]);
    const errorRoutes: NonNullable<RoutingManifest["errorRoutes"]> = {};
    if (notFoundKey !== undefined) errorRoutes.notFound = notFoundKey;
    if (notFoundFlightKey !== undefined) {
      errorRoutes.notFoundFlight = notFoundFlightKey;
    }
    if (serverErrorKey !== undefined) errorRoutes.serverError = serverErrorKey;

    const imagesField: Partial<Pick<RoutingManifest, "images">> = images
      ? { images: { ...images, configHash: imageConfigHash(images) } }
      : {};

    const vercelCacheField: Partial<Pick<RoutingManifest, "vercelCacheAlias">> =
      process.env.OCEL_E2E_VERCEL_CACHE_HEADER === "1"
        ? { vercelCacheAlias: true }
        : {};

    const middlewareField: Partial<Pick<RoutingManifest, "middleware">> =
      middleware
        ? {
            middleware: originMiddleware
              ? {
                  runtime: "nodejs",
                  id: seededBundleId!,
                  entryKey: middlewareEntryKey,
                  matchers: originMiddleware.config.matchers ?? [],
                }
              : {
                  runtime: "edge",
                  entryKey: edgeEntryOf(middleware).entryKey,
                  matchers: middleware.config.matchers ?? [],
                },
          }
        : {};

    const errorRoutesField: Partial<Pick<RoutingManifest, "errorRoutes">> =
      Object.keys(errorRoutes).length > 0 ? { errorRoutes } : {};

    const routingManifest = exactly<RoutingManifest>()({
      entry,
      buildId,
      appName,
      basePath: config.basePath || "",
      trailingSlash: !!config.trailingSlash,
      skipTrailingSlashRedirect: !!config.skipTrailingSlashRedirect,
      skipMiddlewareUrlNormalize: !!config.skipMiddlewareUrlNormalize,
      i18n: (config.i18n ?? undefined) as RoutingManifest["i18n"],
      ...imagesField,

      ...vercelCacheField,

      assetHashes,
      pathnames: [
        ...new Set([
          ...allRoutes.map((o) => routeKeyOf(o, basePath)),
          ...outputs.prerenders.map((p) => p.pathname),
          ...outputs.staticFiles.map((o) =>
            staticRouteKeyOf(o, pagesDistDir, basePath),
          ),
          ...publicFiles.map((p) => p.pathname),
          ...nextDataStaticFiles.map((d) => d.dataKey),
        ]),
      ],
      routes: routing,

      ...middlewareField,

      ...errorRoutesField,

      dispatch,
    });

    await mkdir(outputRoot, { recursive: true });
    writeFileSync(
      join(outputRoot, "routing-manifest.json"),
      JSON.stringify(routingManifest),
    );
    const pprRoutes = outputs.prerenders
      .filter((p) => p.pprChain && isUserFacingPathname(p.pathname))
      .map((p) => p.pathname);
    const cachedRoutes = outputs.prerenders.filter((p) =>
      isUserFacingPathname(p.pathname),
    );
    const streamedRoutes = outputs.appPages.filter((p) =>
      isUserFacingPathname(p.pathname),
    );
    const edgeNeedRoutes = edgeRoutes.filter((r) =>
      isUserFacingPathname(r.pathname),
    );
    const needs: ServeDescriptor["needs"] = {
      ...(middleware?.runtime === "edge" && {
        "edge-middleware": {
          count: 1,
          matchers: (middleware.config.matchers ?? []).map((m) => m.sourceRegex),
        },
      }),
      ...(edgeNeedRoutes.length > 0 && {
        "edge-runtime": {
          count: edgeNeedRoutes.length,
          routes: edgeNeedRoutes.map((r) => routeKeyOf(r, basePath)),
        },
      }),
      ...(pprRoutes.length > 0 && {
        "ppr-resume": { count: pprRoutes.length, routes: pprRoutes },
      }),
      ...(cachedRoutes.length > 0 && {
        "edge-cache": { count: cachedRoutes.length },
      }),
      ...(streamedRoutes.length > 0 && {
        streaming: { count: streamedRoutes.length },
      }),
    };

    const serve: ServeDescriptor = {
      framework: "next",
      buildId,
      edgeRouting: true,
      entry,
      needs,
    };
    writeFileSync(join(outputRoot, "serve.json"), JSON.stringify(serve));

    if (images) {
      await writeFile(
        join(outputRoot, "image-config.json"),
        serializeImageConfig(images),
      );
    }

    await emitEdgeBundle(
      outputRoot,
      distDir,
      assetSuffix,
      programmableEdge
        ? [...edgeRoutes, ...(edgeMiddleware ? [edgeMiddleware] : [])]
        : [],
    );
  },
} satisfies NextAdapter;

type NodeRoute =
  | AdapterOutput["PAGES"]
  | AdapterOutput["PAGES_API"]
  | AdapterOutput["APP_PAGE"]
  | AdapterOutput["APP_ROUTE"];

function configClass(_route: NodeRoute): string {
  return "";
}

function primaryEntryKey(
  members: readonly PackedMember<NodeRoute>[],
): string | null {
  const weighed = [...members].sort(
    (a, b) => b.sizeBytes - a.sizeBytes || (a.member.id < b.member.id ? -1 : 1),
  );
  return weighed[0]?.member.id ?? null;
}

function warnOriginMiddlewareBlastRadius(
  middleware: { config: { matchers?: { sourceRegex: string }[] } },
  cachedPathnames: readonly string[],
): void {
  const matchers = middleware.config.matchers ?? [];
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

type EdgeOutput =
  | AdapterOutput["PAGES"]
  | AdapterOutput["PAGES_API"]
  | AdapterOutput["APP_PAGE"]
  | AdapterOutput["APP_ROUTE"]
  | AdapterOutput["MIDDLEWARE"];

interface EdgeEntry {
  chunks: string[];
  handlerExport: string;
}

function edgeEntryOf(output: EdgeOutput): {
  entryKey: string;
  handlerExport: string;
} {
  if (!output.edgeRuntime) {
    throw new Error(
      `ocel: edge output "${output.pathname}" carries no edgeRuntime entry — this build cannot be served`,
    );
  }
  return output.edgeRuntime;
}

function renderEdgeShim(
  entries: Record<string, EdgeEntry>,
  assetIdByName: Map<string, string>,
  wasmIdByName: Map<string, string>,
  clientAssetSuffix: string,
): string {
  return `import { AsyncLocalStorage } from "node:async_hooks"

globalThis.NEXT_CLIENT_ASSET_SUFFIX = ${JSON.stringify(clientAssetSuffix)}

const ENTRIES = ${stableStringify(entries)}
const ASSETS = ${stableStringify(Object.fromEntries(assetIdByName))}
const WASM = ${stableStringify(Object.fromEntries(wasmIdByName))}

const ocelFetch = globalThis.fetch

const ocelAssetId = (url) => {
  if (typeof url !== "string" || !url.startsWith("blob:")) return undefined
  const name = url.slice(5)
  if (Object.hasOwn(ASSETS, name)) return ASSETS[name]
  // A chunk fetches \`new URL(<blob string>)\`, and URL percent-encodes non-ASCII
  // in an opaque path — so the name can arrive encoded. One that is not valid
  // encoding at all was never one of ours.
  let decoded
  try {
    decoded = decodeURIComponent(name)
  } catch {
    return undefined
  }
  return Object.hasOwn(ASSETS, decoded) ? ASSETS[decoded] : undefined
}

globalThis.fetch = (input, init) => {
  const url =
    typeof input === "string" ? input : input instanceof URL ? input.href : input?.url
  const id = ocelAssetId(url)
  if (id === undefined) return ocelFetch(input, init)
  return import("./" + id).then((m) => new Response(m.default))
}

export default {
  async fetch(request, env, ctx) {
    globalThis.AsyncLocalStorage ??= AsyncLocalStorage
    globalThis.process ??= { env: {} }
    Object.assign(globalThis.process.env, env)
    globalThis.__OCEL_EDGE_CACHE = { rpc: env.OCEL_CACHE_RPC, scope: env.OCEL_CACHE_SCOPE }
    const k = ctx.props.entryKey
    globalThis.__OCEL_EDGE_ENTRY = k
    const e = ENTRIES[k]
    if (!e) return new Response(\`unknown edge entry \${k}\`, { status: 500 })
    for (const [name, id] of Object.entries(WASM)) {
      globalThis[name] ??= (await import("./" + id)).default
    }
    for (const id of e.chunks) await import("./" + id)
    const entry = await globalThis._ENTRIES[k]
    const handler = entry[e.handlerExport]
    return handler(request, {
      waitUntil: (p) => ctx.waitUntil(p),
      signal: request.signal,
      requestMeta: {},
    })
  },
}
`;
}

async function moduleIds(
  pathByKey: Map<string, string>,
  idOf: (index: number) => string,
  encode: (bytes: Buffer) => string,
): Promise<{ idByKey: Map<string, string>; modules: Record<string, string> }> {
  const idByKey = new Map<string, string>();
  const idByHash = new Map<string, string>();
  const modules: Record<string, string> = {};
  for (const key of [...pathByKey.keys()].sort()) {
    const bytes = await readFile(pathByKey.get(key)!);
    const hash = createHash("sha256").update(bytes).digest("hex");
    let id = idByHash.get(hash);
    if (!id) {
      id = idOf(idByHash.size);
      idByHash.set(hash, id);
      modules[id] = encode(bytes);
    }
    idByKey.set(key, id);
  }
  return { idByKey, modules };
}

interface EdgeManifest {
  middleware: Record<string, EdgeManifestFn>;
  functions: Record<string, EdgeManifestFn>;
}

interface EdgeManifestFn {
  name: string;
  assets?: { name: string; filePath: string }[];
}

async function edgeAssetNames(distDir: string): Promise<Set<string> | null> {
  const path = join(distDir, "server", "middleware-manifest.json");
  let manifest: EdgeManifest;
  try {
    manifest = JSON.parse(await readFile(path, "utf8")) as EdgeManifest;
  } catch (error) {
    console.warn(
      `ocel: could not read ${path} (${error instanceof Error ? error.message : String(error)}) — classifying edge assets by file extension instead`,
    );
    return null;
  }

  const names = new Set<string>();
  for (const fn of [
    ...Object.values(manifest.middleware ?? {}),
    ...Object.values(manifest.functions ?? {}),
  ]) {
    for (const asset of fn.assets ?? []) names.add(asset.name);
  }
  return names;
}

interface EdgeParts {
  chunkPathByKey: Map<string, string>;
  wasmPathByName: Map<string, string>;
  assetPathByName: Map<string, string>;
  env: Record<string, string>;
  entryAssets: Map<string, Set<string>>;
  handlerExports: Map<string, string>;
}

async function classifyEdgeSources(
  distDir: string,
  sources: readonly EdgeOutput[],
): Promise<EdgeParts> {
  const assetNames = await edgeAssetNames(distDir);

  const isMap = (key: string) => key.endsWith(".map");
  const isChunk = (key: string) =>
    !isMap(key) &&
    (assetNames ? !assetNames.has(key) : /\.[cm]?js$/.test(key));
  const isAsset = (key: string) => !isMap(key) && !isChunk(key);

  const chunkPathByKey = new Map<string, string>();
  const wasmPathByName = new Map<string, string>();
  const assetPathByName = new Map<string, string>();
  const env: Record<string, string> = {};
  const entryAssets = new Map<string, Set<string>>();
  const handlerExports = new Map<string, string>();

  for (const source of sources) {
    const { entryKey, handlerExport } = edgeEntryOf(source);
    handlerExports.set(entryKey, handlerExport);

    const assets = entryAssets.get(entryKey) ?? new Set<string>();
    entryAssets.set(entryKey, assets);
    for (const [key, abs] of Object.entries(source.assets)) {
      if (isAsset(key)) {
        assetPathByName.set(key, abs);
        continue;
      }
      if (!isChunk(key)) continue;
      chunkPathByKey.set(key, abs);
      assets.add(key);
    }

    for (const [name, abs] of Object.entries(source.wasmAssets ?? {})) {
      wasmPathByName.set(name, abs);
    }

    for (const [key, value] of Object.entries(source.config.env ?? {})) {
      const seen = env[key];
      if (seen !== undefined && seen !== value) {
        throw new Error(
          `ocel: edge outputs disagree on the value of env "${key}" — the bundle holds one env for every entry`,
        );
      }
      env[key] = value;
    }
  }

  return {
    chunkPathByKey,
    wasmPathByName,
    assetPathByName,
    env,
    entryAssets,
    handlerExports,
  };
}

const originEdgeDirName = "__ocel_edge";

const originEdgeRuntimeName = "__ocel_edge_entry.cjs";

interface OriginEdgePlan {
  seedAssets: Record<string, string>;
  files: Record<string, string>;
  entries: Record<string, string>;
}

async function planOriginEdgeEntries(
  distDir: string,
  sources: readonly EdgeOutput[],
  appRel: string,
  clientAssetSuffix: string,
  launcherKeyOf: (edgeEntryKey: string) => string,
): Promise<OriginEdgePlan | null> {
  if (sources.length === 0) return null;

  const parts = await classifyEdgeSources(distDir, sources);

  const seedAssets: Record<string, string> = {};
  const seed = (kind: string, name: string, abs: string): string => {
    const rel = [originEdgeDirName, kind, name].join("/");
    seedAssets[join(appRel, ...rel.split("/"))] = abs;
    return `./${rel}`;
  };

  const chunkRelByKey = new Map<string, string>();
  for (const [key, abs] of parts.chunkPathByKey) {
    chunkRelByKey.set(key, seed("c", key, abs));
  }
  const assets: Record<string, string> = {};
  for (const [name, abs] of parts.assetPathByName) {
    assets[name] = seed("a", name, abs);
  }
  const wasm: Record<string, string> = {};
  for (const [name, abs] of parts.wasmPathByName) {
    wasm[name] = seed("w", `${name}.wasm`, abs);
  }

  const files: Record<string, string> = {
    [`${originEdgeDirName}/package.json`]: JSON.stringify({
      type: "commonjs",
    }),
  };
  const entries: Record<string, string> = {};
  let index = 0;
  for (const edgeEntryKey of [...parts.entryAssets.keys()].sort()) {
    const launcherKey = launcherKeyOf(edgeEntryKey);
    const wrapper = `__ocel_edge_${index++}.cjs`;
    files[wrapper] = renderOriginEdgeWrapper({
      kind: launcherKey === middlewareEntryKey ? "middleware" : "route",
      entryKey: edgeEntryKey,
      handlerExport: parts.handlerExports.get(edgeEntryKey)!,
      chunks: [...parts.entryAssets.get(edgeEntryKey)!].map(
        (key) => chunkRelByKey.get(key)!,
      ),
      assets,
      wasm,
      env: parts.env,
      clientAssetSuffix,
    });
    entries[launcherKey] = `./${wrapper}`;
  }

  return { seedAssets, files, entries };
}

function renderOriginEdgeWrapper(spec: {
  kind: string;
  entryKey: string;
  handlerExport: string;
  chunks: string[];
  assets: Record<string, string>;
  wasm: Record<string, string>;
  env: Record<string, string>;
  clientAssetSuffix: string;
}): string {
  return (
    [
      `module.exports = require(${JSON.stringify(`./${originEdgeRuntimeName}`)}).entry(`,
      `  __dirname,`,
      `  ${stableStringify(spec)},`,
      `)`,
    ].join("\n") + "\n"
  );
}

async function emitEdgeBundle(
  outputRoot: string,
  distDir: string,
  clientAssetSuffix: string,
  sources: readonly EdgeOutput[],
): Promise<void> {
  if (sources.length === 0) return;

  const {
    chunkPathByKey,
    wasmPathByName,
    assetPathByName,
    env,
    entryAssets,
    handlerExports,
  } = await classifyEdgeSources(distDir, sources);

  const { idByKey: chunkIdByKey, modules: chunks } = await moduleIds(
    chunkPathByKey,
    (n) => `c/${n}.js`,
    (bytes) => bytes.toString("utf8"),
  );
  const { idByKey: wasmIdByName, modules: wasmModules } = await moduleIds(
    wasmPathByName,
    (n) => `w/${n}.wasm`,
    (bytes) => bytes.toString("base64"),
  );
  const { idByKey: assetIdByName, modules: assetModules } = await moduleIds(
    assetPathByName,
    (n) => `a/${n}.bin`,
    (bytes) => bytes.toString("base64"),
  );

  const entries: Record<string, EdgeEntry> = {};
  for (const [entryKey, keys] of entryAssets) {
    entries[entryKey] = {
      chunks: [...new Set([...keys].map((k) => chunkIdByKey.get(k)!))],
      handlerExport: handlerExports.get(entryKey)!,
    };
  }

  const json = stableStringify({
    version: 2,
    mainModule: "main.js",
    shim: renderEdgeShim(entries, assetIdByName, wasmIdByName, clientAssetSuffix),
    chunks,
    wasm: wasmModules,
    assets: assetModules,
    env,
    entries,
  });

  const dest = join(outputRoot, "edge", "bundle.json");
  await mkdir(dirname(dest), { recursive: true });
  await writeFile(dest, json);

  const mb = (Buffer.byteLength(json) / 1024 / 1024).toFixed(1);
  console.log(
    `ocel: edge bundle ${mb} MB, ${Object.keys(chunks).length} chunks, ${Object.keys(assetModules).length} assets, ${Object.keys(entries).length} entries`,
  );
}

function cacheTags(prerender: AdapterOutput["PRERENDER"]): string[] {
  const header = prerender.fallback?.initialHeaders?.["x-next-cache-tags"];
  const raw = Array.isArray(header) ? header.join(",") : header;
  if (!raw) return [];

  const { tags, dropped } = boundCacheTags(raw.split(","));
  if (dropped > 0) {
    console.warn(
      `ocel: dropped ${dropped} cache tag(s) over Cloudflare's limits for "${prerender.pathname}" — purges naming them will not hit this route`,
    );
  }
  return tags;
}

async function patchCacheHandlers(distDir: string): Promise<void> {
  const manifestPath = join(distDir, "required-server-files.json");
  let manifest: { config?: Record<string, unknown> };
  try {
    manifest = JSON.parse(await readFile(manifestPath, "utf8"));
  } catch {
    return;
  }
  if (!manifest.config) return;
  manifest.config.cacheHandler = cacheHandlerPath;
  manifest.config.cacheHandlers = {
    ...(manifest.config.cacheHandlers as Record<string, string> | undefined),
    ...useCacheHandlerPaths,
  };
  await writeFile(manifestPath, JSON.stringify(manifest));
}

interface CacheEntryFile {
  lastModified: number;
  value: Record<string, unknown>;
}

function segmentPath(pathname: string): string | null {
  const at = pathname.indexOf(".segments/");
  if (at === -1 || !pathname.endsWith(".segment.rsc")) return null;
  return pathname.slice(at + ".segments".length, -".segment.rsc".length);
}

async function readMaybe(filePath: string | undefined): Promise<Buffer | null> {
  if (!filePath) return null;
  try {
    return await readFile(filePath);
  } catch {
    return null;
  }
}

interface PrerenderGroup {
  entryKey: string;
  html: any;
  rsc: any;
  data: any;
  segments: any[];
}

const NEXT_DATA_PRERENDER = /\/_next\/data\/[^/]+\/.*\.json$/;

function isUserFacingPathname(pathname: string): boolean {
  return (
    segmentPath(pathname) === null &&
    !pathname.endsWith(".rsc") &&
    !NEXT_DATA_PRERENDER.test(pathname)
  );
}

function groupPrerenders(prerenders: readonly any[]): PrerenderGroup[] {
  const byGroup = new Map<number, any[]>();
  for (const p of prerenders) {
    const members = byGroup.get(p.groupId);
    if (members) members.push(p);
    else byGroup.set(p.groupId, [p]);
  }

  const groups: PrerenderGroup[] = [];
  for (const members of byGroup.values()) {
    const segments = members.filter((m) => segmentPath(m.pathname) !== null);
    const pages = members.filter((m) => segmentPath(m.pathname) === null);
    const html = pages.find((m) => isUserFacingPathname(m.pathname));
    if (!html) continue;
    groups.push({
      entryKey: cacheKey(html.pathname),
      html,
      rsc: pages.find((m) => m.pathname.endsWith(".rsc")),
      data: pages.find((m) => NEXT_DATA_PRERENDER.test(m.pathname)),
      segments,
    });
  }
  return groups;
}

function variantHeadersOf(group: PrerenderGroup): Record<string, unknown> {
  const rscHeaders = group.rsc?.fallback?.initialHeaders;
  const segmentHeaders = group.segments.find((m) => m.fallback?.initialHeaders)
    ?.fallback.initialHeaders;
  return {
    ...(rscHeaders && { rscHeaders }),
    ...(segmentHeaders && { segmentHeaders }),
  };
}

function variantHeaderProjection(
  groups: readonly PrerenderGroup[],
): Record<string, Record<string, unknown>> {
  const projection: Record<string, Record<string, unknown>> = {};
  for (const group of groups) {
    const headers = variantHeadersOf(group);
    if (Object.keys(headers).length > 0) projection[group.entryKey] = headers;
  }
  return projection;
}

async function emitCacheEntries(
  outputRoot: string,
  groups: readonly PrerenderGroup[],
  kindById: ReadonlyMap<string, string>,
): Promise<void> {
  const lastModified = Date.now();

  await Promise.all(
    groups.map(async (group) => {
      const { entryKey, html, rsc, data, segments } = group;
      const body = await readMaybe(html.fallback?.filePath);
      if (!body) return;

      const kind = kindById.get(html.parentOutputId) ?? "APP_PAGE";

      const value: Record<string, unknown> = {
        kind,
        status: html.fallback.initialStatus,
      };

      if (kind === "APP_ROUTE") {
        value.headers = html.fallback.initialHeaders;
        value.body = body.toString("base64");
      } else {
        value.headers = html.fallback.initialHeaders;
        value.html = body.toString("utf8");
        const rscBody = await readMaybe(rsc?.fallback?.filePath);
        if (rscBody) value.rscData = rscBody.toString("base64");
        const dataBody = await readMaybe(data?.fallback?.filePath);
        if (dataBody) value.pageData = JSON.parse(dataBody.toString("utf8"));
        Object.assign(value, variantHeadersOf(group));

        const segmentData: Record<string, string> = {};
        for (const m of segments) {
          const segBody = await readMaybe(m.fallback?.filePath);
          if (segBody) {
            segmentData[segmentPath(m.pathname)!] = segBody.toString("base64");
          }
        }
        if (Object.keys(segmentData).length > 0) value.segmentData = segmentData;
        if (html.fallback.postponedState !== undefined) {
          value.postponed = html.fallback.postponedState;
        }
      }

      const dest = join(outputRoot, "cache", `${entryKey}.cache.json`);
      await mkdir(dirname(dest), { recursive: true });
      const entry: CacheEntryFile = { lastModified, value };
      await writeFile(dest, JSON.stringify(entry));
    }),
  );
}

async function emitFetchEntries(
  outputRoot: string,
  distDir: string,
): Promise<void> {
  const fetchCacheDir = join(distDir, "cache", "fetch-cache");
  let names: string[];
  try {
    names = await readdir(fetchCacheDir);
  } catch {
    return; // An app that cached no fetch has no directory at all.
  }

  const lastModified = Date.now();

  await Promise.all(
    names.map(async (name) => {
      const src = join(fetchCacheDir, name);
      const [raw, stats] = await Promise.all([
        readFile(src, "utf8").catch(() => null),
        lstat(src).catch(() => null),
      ]);
      if (raw === null || !stats?.isFile()) return;

      let value: Record<string, unknown>;
      try {
        value = JSON.parse(raw);
      } catch {
        return; // A half-written entry is a miss, not a failed build.
      }

      const revalidate = value.revalidate;
      if (
        typeof revalidate === "number" &&
        stats.mtimeMs + revalidate * 1000 <= lastModified
      ) {
        return;
      }

      const dest = join(outputRoot, "fetch-cache", `${name}.cache.json`);
      await mkdir(dirname(dest), { recursive: true });
      const entry: CacheEntryFile = { lastModified, value };
      await writeFile(dest, JSON.stringify(entry));
    }),
  );
}

export interface RouteTable {
  exact: Record<string, string>;
  dynamic: [string, string][];
}

function routeTable(
  entryKeyByPathname: ReadonlyMap<string, string>,
  dynamicRoutes: readonly { sourceRegex: string; destination?: string }[],
): RouteTable {
  const exact = Object.fromEntries(entryKeyByPathname);

  const dynamic: [string, string][] = [];
  for (const route of dynamicRoutes) {
    const page = route.destination?.split("?")[0];
    if (!page || page.includes("$")) continue;
    const entryKey = entryKeyByPathname.get(page);
    if (entryKey !== undefined) dynamic.push([route.sourceRegex, entryKey]);
  }

  return { exact, dynamic };
}

interface NextConfigProjection {
  basePath: string;
  i18n: unknown;
  trailingSlash: boolean;
  experimental: unknown;
  clientAssetSuffix: string;
}

function clientAssetSuffix(config: {
  deploymentId?: string;
  experimental?: { immutableAssetToken?: string };
}): string {
  const token = config.experimental?.immutableAssetToken || config.deploymentId;
  return token ? `?dpl=${token}` : "";
}

function renderLauncher(
  entries: Record<string, string>,
  primary: string | null,
  routes: RouteTable,
  nextConfig: NextConfigProjection,
): string {
  return (
    [
      `const { AsyncLocalStorage } = require('node:async_hooks')`,
      `globalThis.AsyncLocalStorage = AsyncLocalStorage`,
      `process.env.NODE_ENV ||= 'production'`,
      `const ENTRIES = ${JSON.stringify(entries)}`,
      `const PRIMARY = ${JSON.stringify(primary)}`,
      `const ROUTES = ${JSON.stringify(routes)}`,
      `const NEXT_CONFIG = ${JSON.stringify(nextConfig)}`,
      `module.exports = require(${JSON.stringify(`./${dispatchName}`)})({`,
      `  entries: ENTRIES,`,
      `  primary: PRIMARY,`,
      `  routes: ROUTES,`,
      `  nextConfig: NEXT_CONFIG,`,
      `  load: (specifier) => require(specifier),`,
      `})`,
    ].join("\n") + "\n"
  );
}

function routePathname(
  pathname: string,
  basePath: string,
  strip: (pathname: string) => string = unindex,
): string {
  if (!basePath) return strip(pathname);
  if (pathname === basePath) return basePath;
  if (!pathname.startsWith(`${basePath}/`)) return pathname;
  const fixed = strip(pathname.slice(basePath.length));
  return fixed === "/" ? basePath : `${basePath}${fixed}`;
}

const dynamicSegment = /\/\[[^/]+\](?=\/|$)/;

function unindex(pathname: string): string {
  if (dynamicSegment.test(pathname)) return pathname;
  if (pathname === "/index") return "/";
  if (pathname.startsWith("/index/")) return pathname.slice("/index".length);
  return pathname;
}

function unindexLeaf(pathname: string): string {
  if (dynamicSegment.test(pathname)) return pathname;
  if (pathname === "/index") return "/";
  return pathname.endsWith("/index")
    ? pathname.slice(0, -"/index".length)
    : pathname;
}

function isPagesRouterKind(type: string): boolean {
  return type === "PAGES" || type === "PAGES_API";
}

// TODO: drop once Next stops classifying by `page.startsWith('/api')` in
// build-complete.ts — that check hands a pages route like /api-docs/[...slug] the
// PAGES_API kind, and an API route never parents a prerender.
function routeKindsById(
  routes: readonly { id: string; type: string }[],
  prerenders: readonly { parentOutputId: string }[],
): Map<string, string> {
  const prerendered = new Set(prerenders.map((p) => p.parentOutputId));
  return new Map(
    routes.map((r) => [
      r.id,
      r.type === "PAGES_API" && prerendered.has(r.id) ? "PAGES" : r.type,
    ]),
  );
}

function isDocumentRouteKind(type: string): boolean {
  return type === "PAGES" || type === "APP_PAGE";
}

function routeKeyOf(
  output: { pathname: string; type: string; runtime: string },
  basePath: string,
): string {
  if (!isPagesRouterKind(output.type)) return output.pathname;
  return routePathname(
    output.pathname,
    basePath,
    output.runtime === "nodejs" ? unindex : unindexLeaf,
  );
}

function staticRouteKeyOf(
  file: { pathname: string; filePath: string },
  pagesDistDir: string,
  basePath: string,
): string {
  return file.filePath.startsWith(pagesDistDir)
    ? routePathname(file.pathname, basePath)
    : file.pathname;
}

function nextDataPathnameOf(
  pageKey: string,
  buildId: string,
  basePath: string,
): string {
  const unprefixed =
    basePath && pageKey.startsWith(basePath)
      ? pageKey.slice(basePath.length) || "/"
      : pageKey;
  const normalized = unprefixed === "/" ? "/index" : unprefixed;
  const dataPathname = `/_next/data/${buildId}${normalized}.json`;
  return basePath ? `${basePath}${dataPathname}` : dataPathname;
}

function servedPathname(file: { pathname: string; filePath: string }): string {
  return file.filePath.endsWith(".html") && !file.pathname.endsWith(".html")
    ? `${file.pathname}.html`
    : file.pathname;
}

async function copyHashedFile(src: string, dest: string): Promise<string> {
  await mkdir(dirname(dest), { recursive: true });
  const hash = createHash("sha256");
  await pipeline(
    createReadStream(src),
    async function* (chunks) {
      for await (const chunk of chunks) {
        hash.update(chunk);
        yield chunk;
      }
    },
    createWriteStream(dest, { mode: (await stat(src)).mode }),
  );
  return hash.digest("hex");
}

async function writeHashedFile(dest: string, content: string): Promise<string> {
  await mkdir(dirname(dest), { recursive: true });
  await writeFile(dest, content);
  return createHash("sha256").update(content).digest("hex");
}

async function collectPublicFiles(
  projectDir: string,
): Promise<{ pathname: string; filePath: string }[]> {
  const publicDir = join(projectDir, "public");
  let entries;
  try {
    entries = await readdir(publicDir, {
      recursive: true,
      withFileTypes: true,
    });
  } catch {
    return [];
  }
  const files: { pathname: string; filePath: string }[] = [];
  for (const entry of entries) {
    if (!entry.isFile()) continue;
    const abs = join(entry.parentPath, entry.name);
    const rel = relative(publicDir, abs);
    files.push({ pathname: "/" + rel.split(sep).join("/"), filePath: abs });
  }
  return files;
}

async function copyAsset(srcAbs: string, dest: string): Promise<void> {
  let info;
  try {
    info = await lstat(srcAbs);
  } catch {
    return;
  }
  await mkdir(dirname(dest), { recursive: true });
  if (info.isSymbolicLink()) {
    await rm(dest, { recursive: true, force: true });
    await symlink(await readlink(srcAbs), dest);
    return;
  }
  if (info.isDirectory()) {
    await cp(srcAbs, dest, { recursive: true });
    return;
  }
  await copyFile(srcAbs, dest);
}

export default adapter;
