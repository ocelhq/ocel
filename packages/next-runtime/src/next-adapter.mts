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

const launcherName = "__next_launcher.cjs";
const dispatchName = "__ocel_dispatch.cjs";
// Read back at the root of the function's own task directory by the cache
// handler the membrane layer mounts, which spells this name for itself.
const variantHeadersName = "variant-headers.json";

// The ocel builder passes this app's own subtree; a bare `next build` outside
// ocel falls back to the flat cwd path.
function resolveOutputRoot(): string {
  return process.env.OCEL_OUTPUT_DIR || join(process.cwd(), ".ocel/output");
}

// Where the membrane layer mounts the bundled cache handlers — the ones the
// Lambda tier loads at runtime. These reach S3 with the function's own
// credentials, which `next build`'s static generation workers do not have.
//
// The two tiers are named different handlers through different mechanisms.
// modifyConfig names the edge tier's, the file Turbopack compiles into the edge
// chunks. The node tier's are these paths, patched into the built manifest
// afterwards (see patchCacheHandlers), because Next records a configured handler
// relative to the *build* machine's distDir — a value that does not survive the
// move to /var/task.
//
// The singular `cacheHandler` is the incremental cache (ISR, prerenders, Pages
// Router); the plural `cacheHandlers` map, keyed by cache kind, is what backs
// the `use cache` directive. They are separate contracts and separate modules.
const cacheHandlerPath = "/opt/ocel/next/cache-handler.cjs";
const useCacheHandlerPaths = {
  default: "/opt/ocel/next/use-cache-default.cjs",
  remote: "/opt/ocel/next/use-cache-remote.cjs",
};

// installCacheHandler puts the edge/build cache handler inside the app's own
// tree and returns the path to name it by. It has to live there: Turbopack
// rewrites config.cacheHandler to a project-relative path, and both it and
// `next build`'s page-data workers resolve the handler's bare
// `require('next/…')` from where the file physically sits — so a copy inside
// this package would bind the app's build to this package's Next rather than the
// app's. modifyConfig runs inside loadConfig, well before the config is
// serialized for the build, so writing the file here is early enough.
//
// `next build` runs with the app directory as its cwd, which is what makes the
// project root reachable from a hook that is handed only the config.
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
        // Singular only. The plural map is compiled into every edge chunk as
        // well, and the `use cache` handlers reach the AWS SDK transitively —
        // naming them here would put it in every edge bundle.
        cacheHandler: await installCacheHandler(),
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

    const allRoutes = [
      ...outputs.pages,
      ...outputs.pagesApi,
      ...outputs.appPages,
      ...outputs.appRoutes,
    ];

    const routableOutputs = [
      ...allRoutes,
      ...outputs.prerenders,
      ...outputs.staticFiles,
    ];

    const { middleware } = outputs;
    if (middleware?.runtime === "nodejs") {
      throw new Error(
        `ocel: the nodejs middleware runtime is not supported — ${middleware.sourcePage || middleware.filePath} must export \`config = { runtime: 'edge' }\``,
      );
    }

    const images = compileImageConfig(config.images);

    const functionRoutes = allRoutes.filter((r) => r.runtime === "nodejs");
    const edgeRoutes = allRoutes.filter((r) => r.runtime === "edge");

    // Which entry a prerender's parent renders through, for the prerenders
    // parented by an edge route: they have no Lambda to regenerate from, so the
    // worker invokes this entry instead of looking for a Function URL.
    const edgeEntryByOutputId = new Map(
      edgeRoutes.map((r) => [r.id, edgeEntryOf(r).entryKey]),
    );
    const inertPrerenders = outputs.prerenders
      .filter((p) => edgeEntryByOutputId.has(p.parentOutputId))
      .map((p) => p.pathname);
    if (inertPrerenders.length > 0) {
      console.warn(
        `ocel: revalidate is inert for edge-rendered route(s) ${inertPrerenders.join(", ")} — edge ISR is not supported yet (bd ocelhq-b7l)`,
      );
    }

    const outputRoot = resolveOutputRoot();
    const appRel = relative(repoRoot, projectDir);

    // The ocel app name keys this app's assets in the account-global bucket
    // (<env>/<project>/<appName>/<buildId>/…) and records each function's
    // owning app in its config.json. The ocel builder passes it via
    // OCEL_APP_NAME; falling back to the project dir name keeps a bare
    // `next build` self-consistent.
    const appName = process.env.OCEL_APP_NAME || basename(projectDir);

    // Patch the built manifest before anything is copied out of distDir, so
    // every `.func` picks the cache handler up through the normal asset copy.
    await patchCacheHandlers(distDir);

    // Routes sharing a filePath and config are the same compiled function —
    // e.g. a page and its `.rsc` variant — so they resolve to one entry inside
    // whichever bundle carries them. The group's entry key is its shortest
    // pathname's id: the base route the variants extend, and the id prerenders
    // reference via parentOutputId.
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
        entryKeyByPathname.set(m.pathname, entry.id);
        entryKeyByRouteId.set(m.id, entry.id);
      }
    }

    // A route's compiled module is an asset like any other here: it is what the
    // launcher requires, and leaving it out of the union would hide every
    // route's own bytes from the size accounting the packing rests on.
    const assetsOf = (route: NodeRoute) => ({
      ...route.assets,
      [relative(repoRoot, route.filePath)]: route.filePath,
    });

    const { bundles, missingAssets } = packBundles(entryRoutes, {
      entryKeyOf: (route) => route.id,
      assetsOf,
      partitionBy: configClass,
    });

    const bundleNameByEntryKey = new Map(
      bundles.flatMap((b) =>
        b.members.map((m) => [m.member.id, b.name] as const),
      ),
    );

    // A manifest that names a bundle no build emitted is a 502 per request for
    // that route, in production, and nothing on either side of the manifest can
    // catch it — so an unmappable route fails the build instead.
    const bundleNameOf = (entryKey: string, pathname: string): string => {
      const name = bundleNameByEntryKey.get(entryKey);
      if (name === undefined) {
        throw new Error(
          `ocel: route "${pathname}" resolves to entry "${entryKey}", which no emitted bundle carries — this build cannot be served`,
        );
      }
      return name;
    };

    const prerenderGroups = groupPrerenders(outputs.prerenders);

    // Shipped in the bundle rather than fetched at runtime: a Lambda for build N
    // can then only ever read build N's projection, which is what makes the
    // reseeded headers the ones its own build prerendered.
    const variantHeaders = JSON.stringify(
      variantHeaderProjection(prerenderGroups),
    );

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

        const launcherRel = join(appRel, launcherName);
        await writeFile(
          join(funcDir, launcherRel),
          renderLauncher(
            Object.fromEntries(
              bundle.members.map(({ member }) => [
                member.id,
                "./" +
                  relative(projectDir, member.filePath).split(sep).join("/"),
              ]),
            ),
            primaryEntryKey(bundle.members),
          ),
        );

        await writeFile(
          join(funcDir, "config.json"),
          JSON.stringify({
            runtime: "nodejs24.x",
            handler: launcherRel,
            framework: "next",
            // The bundle's identity, carried through to
            // ManifestFunction.route_id so the routing layer can key
            // FUNCTION_URLS by it (the Lambda itself keeps an infra-safe name).
            id: bundle.name,
            app: appName,
          }),
        );

        await writeFile(join(funcDir, variantHeadersName), variantHeaders);
      }),
    );

    // A traced asset whose source is gone ships a bundle missing a module some
    // route requires — and if the primed entry requires it, every route in that
    // bundle. It predates bundling and is not known to be fatal, so it is loud
    // rather than a failed build: one aggregate line, never one per file. The
    // packer names them because it sized them; the copy skipped exactly these.
    if (missingAssets.length > 0) {
      const sample = missingAssets.slice(0, 5);
      console.warn(
        `ocel: ${missingAssets.length} traced asset(s) have no source on disk and were not copied into the bundle — a route requiring one of them fails at runtime: ${sample.join(", ")}${missingAssets.length > sample.length ? ", …" : ""}`,
      );
    }

    // public/ assets. Next's outputs.staticFiles covers _next/static and the
    // prerendered error pages but never the project's public/ directory, so the
    // adapter copies it verbatim into the static output and makes each file
    // routable — otherwise a request for e.g. /favicon.svg has no dispatch entry
    // and 404s despite the file existing.
    //
    // Both sets are hashed on the way through: the image cache key identifies a
    // local source by its content hash, so identical bytes keep their optimized
    // variants across a redeploy and changed bytes cannot serve a stale one.
    const publicFiles = await collectPublicFiles(projectDir);
    const assetHashes: Record<string, string> = {};
    for (const file of [...publicFiles, ...outputs.staticFiles]) {
      const pathname = servedPathname(file.pathname);
      assetHashes[pathname] = await copyHashedFile(
        file.filePath,
        join(outputRoot, "static", pathname),
      );
    }

    // Seed each prerendered route's cache entry from the build output.
    await emitCacheEntries(outputRoot, prerenderGroups, allRoutes);
    await emitFetchEntries(outputRoot, distDir);

    const routingManifest = {
      buildId,
      appName,
      basePath: config.basePath || "",
      i18n: config.i18n ?? undefined,
      ...(images && {
        images: { ...images, configHash: imageConfigHash(images) },
      }),
      assetHashes,
      pathnames: [
        ...new Set([
          ...routableOutputs.map((o) => o.pathname),
          ...publicFiles.map((p) => p.pathname),
        ]),
      ],
      routes: routing,

      // An absent or empty matcher list is Next's "run on everything" — the
      // shape a bare middleware.ts with no `config` export produces.
      ...(middleware && {
        middleware: {
          entryKey: edgeEntryOf(middleware).entryKey,
          matchers: middleware.config.matchers ?? [],
        },
      }),

      dispatch: Object.fromEntries([
        // One bundle serves many routes, so the id names the Lambda and the
        // entry key names which of its routes to render.
        ...functionRoutes.map((o) => {
          const entryKey = entryKeyByPathname.get(o.pathname);
          if (entryKey === undefined) {
            throw new Error(
              `ocel: node route "${o.pathname}" resolves to no bundle entry — this build cannot be served`,
            );
          }
          return [
            o.pathname,
            {
              kind: "lambda",
              id: bundleNameOf(entryKey, o.pathname),
              entryKey,
            },
          ];
        }),
        // Edge variants (`.rsc`, `_next/data`) share one compiled entry, so
        // several pathnames simply name the same entryKey — a different
        // namespace from the node bundles' entry keys.
        ...edgeRoutes.map((o) => [
          o.pathname,
          { kind: "edge", entryKey: edgeEntryOf(o).entryKey },
        ]),
        ...outputs.staticFiles.map((o) => [o.pathname, { kind: "static" }]),
        ...publicFiles.map((p) => [p.pathname, { kind: "static" }]),

        // A prerendered pathname resolves to a prerender: its cache entry lives
        // in the asset bucket (keyed by build id) and its id is the bundle
        // carrying the parent output — the base route deployed as a Lambda that
        // regenerates the entry. Spread last so it replaces the plain lambda
        // entry a prerendered function route also produced above.
        //
        // The renderer is named by parentOutputId, never by the prerender's own
        // pathname: a segment prerender has no route of its own, a prerender's
        // pathname can collide with a different group's function pathname, and
        // parentOutputId can name a non-representative member of its group —
        // which the by-route-id map resolves to that group's entry.
        //
        // The fallback is projected down to the two freshness windows rather
        // than spread: the shell, the postponed state, and the entry's own
        // status/headers all travel in the cache entry, so carrying build-time
        // copies here would only put a stale second source of truth (and, for
        // postponedState, ~96KB of it per route) in front of every request.
        ...outputs.prerenders.map((p) => {
          const allowQuery = p.config?.allowQuery;
          const tags = cacheTags(p);
          const entryKey = entryKeyByRouteId.get(p.parentOutputId);
          const edgeEntryKey = edgeEntryByOutputId.get(p.parentOutputId);
          if (entryKey === undefined && edgeEntryKey === undefined) {
            throw new Error(
              `ocel: prerender "${p.pathname}" is parented by output "${p.parentOutputId}", which renders on neither a Lambda nor the edge — nothing can regenerate it`,
            );
          }

          return [
            p.pathname,
            {
              kind: "prerender",
              // An edge-parented prerender has no bundle at all: the worker
              // regenerates it through edgeEntryKey and never reads the id, so
              // it carries the parent output's own name for legibility.
              id:
                entryKey === undefined
                  ? p.parentOutputId
                  : bundleNameOf(entryKey, p.pathname),
              config: p.config,
              fallback: {
                initialRevalidate: p.fallback?.initialRevalidate,
                initialExpiration: p.fallback?.initialExpiration,
              },
              ...(p.pprChain && { pprChain: p.pprChain }),
              ...(tags.length > 0 && { tags }),
              ...(allowQuery && { allowQuery }),
              // Which entry of the parent's bundle regenerates this route.
              ...(entryKey !== undefined && { entryKey }),
              // Set only when the parent renders on the edge: there is no
              // Function URL to fall back to, so the worker regenerates this
              // route through the edge bundle's entry instead — and reads the
              // key's absence as "this tier may revalidate".
              ...(edgeEntryKey !== undefined && { edgeEntryKey }),
            },
          ];
        }),
      ]),
    };

    await mkdir(outputRoot, { recursive: true });
    writeFileSync(
      join(outputRoot, "routing-manifest.json"),
      JSON.stringify(routingManifest),
    );

    // Its own artifact because the account-global optimizer holds no authority:
    // it loads this file from the asset bucket rather than trusting anything the
    // edge sends.
    if (images) {
      await writeFile(
        join(outputRoot, "image-config.json"),
        serializeImageConfig(images),
      );
    }

    await emitEdgeBundle(outputRoot, [
      ...edgeRoutes,
      ...(middleware ? [middleware] : []),
    ]);
  },
} satisfies NextAdapter;

// Every route that runs on Lambda.
type NodeRoute =
  | AdapterOutput["PAGES"]
  | AdapterOutput["PAGES_API"]
  | AdapterOutput["APP_PAGE"]
  | AdapterOutput["APP_ROUTE"];

// Routes that cannot share a Lambda. Becomes the route's (maxDuration,
// preferredRegion) class once those reach the manifest (bd ocelhq-kay2): both
// are function-level settings, so routes in one bundle cannot disagree on them.
function configClass(_route: NodeRoute): string {
  return "";
}

// The entry required at INIT, which primes the chunk graph its bundle-mates
// share: the one tracing the most bytes of it, since that graph's cost is bytes
// and not file count. The bytes are the packer's own — the same sizing the
// budget rests on, so the election and the accounting cannot disagree about what
// a route weighs. Ties broken by entry key so an unchanged build produces
// byte-identical output.
function primaryEntryKey(
  members: readonly PackedMember<NodeRoute>[],
): string | null {
  const weighed = [...members].sort(
    (a, b) => b.sizeBytes - a.sizeBytes || (a.member.id < b.member.id ? -1 : 1),
  );
  return weighed[0]?.member.id ?? null;
}

// Every output that runs on the edge: the `runtime: 'edge'` routes and, when the
// app has one, middleware.
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

// The bundle's main module. Turbopack's edge chunks are classic scripts that
// register themselves on globalThis, so nothing here imports by specifier — the
// shim declares every chunk as a module and imports only the hit entry's,
// leaving the rest for workerd to never compile. process.env must be populated
// before the first import: the chunks read it at module scope.
//
// AsyncLocalStorage is a global to Next's edge runtime, never an import — a
// chunk that reaches for it and finds nothing throws the "accessed in runtime
// where it is not available" invariant on evaluation. renderLauncher hands the
// Lambda the same global for the same reason.
//
// The running entry key travels the same way, and for the same reason: a
// variable is read as a plain property, so `ocel/env`'s edge build has nowhere
// to be handed the entry whose read it is about to refuse. It is set before the
// chunks evaluate because a module-scope read is still a read.
//
// The cache binding travels the same way rather than through process.env, whose
// edge reads are build-time string literals a binding could not survive. It is
// read from the load-time env — the main worker's own ctx.exports loopback,
// which outlives every request — and never from the request's ctx: the isolate
// is cached, so a stub captured from whichever request cold-started it is
// disposed at that request's end and leaves requests 2..N holding a dead one.
function renderEdgeShim(entries: Record<string, EdgeEntry>): string {
  return `import { AsyncLocalStorage } from "node:async_hooks"

const ENTRIES = ${stableStringify(entries)}

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

// EDGE_ENV_MARKER is the name `ocel/env`'s edge build gives the error it throws
// on a read. It is the load-bearing string of that module rather than a marker
// planted for this scan, so it survives the bundler for the same reason the
// error does — and if the module is tree-shaken out entirely, the read it would
// have refused does not exist either.
const EDGE_ENV_MARKER = "EnvEdgeError";

// warnEdgeEnvRoutes reports the edge entries that carry `ocel/env`. No variable
// class is deliverable to the edge tier, so a read from one of these throws at
// request time; this is what says so at build time instead, naming the route.
//
// It is a warning and not a build failure on purpose: a chunk shows that the
// module was *imported*, never that a variable was read. An edge route
// importing a barrel that re-exports `env` without touching it is legitimate,
// and failing it would be wrong.
function warnEdgeEnvRoutes(
  chunkKeysByPathname: Map<string, string[]>,
  chunkIdByKey: Map<string, string>,
  chunks: Record<string, string>,
): void {
  const pathnames = [...chunkKeysByPathname]
    .filter(([, keys]) =>
      keys.some((key) => chunks[chunkIdByKey.get(key)!]?.includes(EDGE_ENV_MARKER)),
    )
    .map(([pathname]) => pathname)
    .sort();
  if (pathnames.length === 0) return;

  console.warn(
    `ocel: ocel/env is imported by edge entr${pathnames.length === 1 ? "y" : "ies"} ${pathnames.join(", ")} — no variable class is deliverable to the edge runtime, so reading one there throws on the first request. Move the entry to the nodejs runtime.`,
  );
}

// moduleIds reads every source file and gives each distinct content one opaque
// module id, so a chunk two entries share is carried once. Sources are visited
// in sorted-key order and ids are numbered as they are minted, which is what
// makes the bundle byte-stable across builds with unchanged input.
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

// emitEdgeBundle writes the single JSON file the main worker turns into a
// Cloudflare dynamic worker: every edge chunk under an opaque module id, the
// wasm those chunks need, the env they read, and the table mapping each entry
// key to what must be evaluated before its handler can be called. Ids are
// content-deduped and assigned in sorted-key order so an unchanged build yields
// an identical file — the deployment's worker id is a hash of these bytes.
async function emitEdgeBundle(
  outputRoot: string,
  sources: readonly EdgeOutput[],
): Promise<void> {
  if (sources.length === 0) return;

  // Source maps are dead weight in the bundle — one alone runs to 1.5 MB.
  const isChunk = (key: string) => !key.endsWith(".map");

  const chunkPathByKey = new Map<string, string>();
  const wasmPathByName = new Map<string, string>();
  const env: Record<string, string> = {};
  const entryAssets = new Map<string, Set<string>>();
  const handlerExports = new Map<string, string>();
  const chunkKeysByPathname = new Map<string, string[]>();

  for (const source of sources) {
    const { entryKey, handlerExport } = edgeEntryOf(source);
    handlerExports.set(entryKey, handlerExport);

    const assets = entryAssets.get(entryKey) ?? new Set<string>();
    entryAssets.set(entryKey, assets);
    const ownChunkKeys: string[] = [];
    for (const [key, abs] of Object.entries(source.assets)) {
      if (!isChunk(key)) continue;
      chunkPathByKey.set(key, abs);
      assets.add(key);
      ownChunkKeys.push(key);
    }
    // Two sources can share a pathname (a route and its middleware), and the
    // scan is about the pathname, so their chunks accumulate rather than
    // replace: whichever came first would otherwise stop being looked at.
    chunkKeysByPathname.set(source.pathname, [
      ...(chunkKeysByPathname.get(source.pathname) ?? []),
      ...ownChunkKeys,
    ]);

    // Wasm modules are declared globally in the bundle, not per entry: workerd
    // compiles a declared-but-unimported module lazily, so an entry that needs
    // none pays nothing for the rest.
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

  const { idByKey: chunkIdByKey, modules: chunks } = await moduleIds(
    chunkPathByKey,
    (n) => `c/${n}.js`,
    (bytes) => bytes.toString("utf8"),
  );
  const { modules: wasmModules } = await moduleIds(
    wasmPathByName,
    (n) => `w/${n}.wasm`,
    (bytes) => bytes.toString("base64"),
  );

  warnEdgeEnvRoutes(chunkKeysByPathname, chunkIdByKey, chunks);

  // Chunk order is load order, never sorted: a Turbopack chunk evaluates its
  // modules as it registers, so one that runs before the chunk defining a module
  // it requires fails with that module's factory unavailable. Next lists a
  // page's files in the order they must be evaluated and `assets` preserves it,
  // so the entry carries them in the order they arrived. Determinism comes from
  // that order being the build's own, not from re-sorting it.
  const entries: Record<string, EdgeEntry> = {};
  for (const [entryKey, keys] of entryAssets) {
    entries[entryKey] = {
      chunks: [...new Set([...keys].map((k) => chunkIdByKey.get(k)!))],
      handlerExport: handlerExports.get(entryKey)!,
    };
  }

  const json = stableStringify({
    version: 1,
    mainModule: "main.js",
    shim: renderEdgeShim(entries),
    chunks,
    wasm: wasmModules,
    env,
    entries,
  });

  const dest = join(outputRoot, "edge", "bundle.json");
  await mkdir(dirname(dest), { recursive: true });
  await writeFile(dest, json);

  const mb = (Buffer.byteLength(json) / 1024 / 1024).toFixed(1);
  console.log(
    `ocel: edge bundle ${mb} MB, ${Object.keys(chunks).length} chunks, ${Object.keys(entries).length} entries`,
  );
}

// Cloudflare's Cache-Tag ceilings: 16KB aggregate on the response header, 1000
// tags in it, and 1024 chars per tag in a purge call. Cloudflare rejects an
// over-limit header outright, which would cost the route every one of its tags
// rather than the offending few — so trim to fit here instead.
const maxTagBytes = 1024;
const maxTags = 1000;
const maxTagsBytes = 16 * 1024;

// Next's encodeCacheTag percent-encodes only characters outside [\t\x20-\x7e],
// so spaces and tabs survive it — and Cloudflare's Cache-Tag forbids both.
// Extending Next's own scheme keeps one canonical encoding end to end, and the
// purge side has to apply the same transform to match what was stamped.
function sanitizeCacheTag(tag: string): string {
  return tag.replace(/[\t ]/g, (c) => encodeURIComponent(c));
}

// cacheTags reads the tag set Next recorded for a prerender and returns it in a
// form Cloudflare will accept: sanitized, and trimmed to the header's ceilings.
// A tag over the per-tag limit is dropped rather than truncated — a truncated
// tag matches nothing, so it would occupy budget while never purging.
function cacheTags(prerender: AdapterOutput["PRERENDER"]): string[] {
  const header = prerender.fallback?.initialHeaders?.["x-next-cache-tags"];
  const raw = Array.isArray(header) ? header.join(",") : header;
  if (!raw) return [];

  const parts = raw.split(",");
  const tags: string[] = [];
  let bytes = 0;
  for (const part of parts) {
    const tag = sanitizeCacheTag(part);
    const size = Buffer.byteLength(tag);
    if (!tag || size > maxTagBytes) continue;
    const cost = size + (tags.length > 0 ? 1 : 0);
    if (tags.length >= maxTags || bytes + cost > maxTagsBytes) break;
    bytes += cost;
    tags.push(tag);
  }

  if (tags.length < parts.length) {
    console.warn(
      `ocel: dropped ${parts.length - tags.length} cache tag(s) over Cloudflare's limits for "${prerender.pathname}" — purges naming them will not hit this route`,
    );
  }
  return tags;
}

// patchCacheHandler names the layer's cache handler in the manifest `next build`
// just wrote. The runtime reads this file back as nextConfig and resolves
// cacheHandler through formatDynamicImportPath(distDir, value), which leaves the
// value alone only when it is already absolute — so writing the runtime path
// here, after the build, is what survives the move to /var/task. A build with no
// manifest (`output: 'export'`) has no server to configure.
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

// A route's cache entry as the handler reads it back: Next keys one entry per
// route holding the html, the RSC payload and any PPR segments together, but the
// adapter API surfaces those as separate PRERENDER outputs. Bodies are base64 so
// the entry stays a single JSON object — one GET, one atomic write, and no torn
// entry to serve.
interface CacheEntryFile {
  lastModified: number;
  value: Record<string, unknown>;
}

// segmentPath recovers the key FileSystemCache stores a PPR segment under:
// `<route>.segments/<segmentPath>.segment.rsc`.
function segmentPath(pathname: string): string | null {
  const at = pathname.indexOf(".segments/");
  if (at === -1 || !pathname.endsWith(".segment.rsc")) return null;
  return pathname.slice(at + ".segments".length, -".segment.rsc".length);
}

// readMaybe returns a fallback body, or null when the build did not emit one.
// A prerender can name a body it never wrote (a blocking fallback, say), and an
// entry we cannot seed is not a build failure — the route renders on first
// request and populates the cache itself.
async function readMaybe(filePath: string | undefined): Promise<Buffer | null> {
  if (!filePath) return null;
  try {
    return await readFile(filePath);
  } catch {
    return null;
  }
}

// A route's prerender variants, back together. Next surfaces the html, the
// `.rsc` payload and each PPR segment as separate PRERENDER outputs sharing a
// groupId; everything downstream wants the route.
interface PrerenderGroup {
  // How the cache handler keys this route's entry (e.g. /blog/a → blog/a, / →
  // index). Taken from the html variant's concrete pathname, never from
  // parentOutputId, which names the shared function under its dynamic pattern.
  key: string;
  html: any;
  rsc: any;
  segments: any[];
}

// groupPrerenders recombines each groupId's outputs into the route they
// describe. A member is a segment or the `.rsc` payload by its own suffix; the
// html variant is the one that is neither. A group whose html variant Next did
// not prerender (a blocking fallback) describes no entry and is dropped.
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
    const html = pages.find((m) => !m.pathname.endsWith(".rsc"));
    if (!html) continue;
    groups.push({
      key: html.pathname === "/" ? "index" : html.pathname.replace(/^\//, ""),
      html,
      rsc: pages.find((m) => m.pathname.endsWith(".rsc")),
      segments,
    });
  }
  return groups;
}

// The headers the `.rsc` and segment variants were prerendered with. Each
// variant arrives with its own initialHeaders, and replaying exactly those is
// what lets a client see what Next would have served — including the segment
// cache's x-nextjs-postponed: 2 marker, which lives only on the segment variants
// and is the header PPR support is gated on. The segment variants' headers are
// identical across a group, so the first one seen stands for all of them.
function variantHeadersOf(group: PrerenderGroup): Record<string, unknown> {
  const rscHeaders = group.rsc?.fallback?.initialHeaders;
  const segmentHeaders = group.segments.find((m) => m.fallback?.initialHeaders)
    ?.fallback.initialHeaders;
  return {
    ...(rscHeaders && { rscHeaders }),
    ...(segmentHeaders && { segmentHeaders }),
  };
}

// What a regenerating Lambda reseeds an entry's variant headers from, keyed the
// way it keys the entry itself. They exist only here, in the build's prerender
// output — Next's runtime set() payload carries a single page-level headers map
// — so a revalidation rewrite that could not reach them would drop the segment
// cache markers the edge serves PPR by and the content-type an RSC request is
// negotiated with. Nothing else the routing manifest holds is projected: that is
// read at the edge, and a function bundle carrying it would carry it dead.
function variantHeaderProjection(
  groups: readonly PrerenderGroup[],
): Record<string, Record<string, unknown>> {
  const projection: Record<string, Record<string, unknown>> = {};
  for (const group of groups) {
    const headers = variantHeadersOf(group);
    if (Object.keys(headers).length > 0) projection[group.key] = headers;
  }
  return projection;
}

// emitCacheEntries seeds one cache entry per prerendered route from the build's
// own output, so a deployed route serves its prerender instead of re-rendering
// on the first request to every instance.
async function emitCacheEntries(
  outputRoot: string,
  groups: readonly PrerenderGroup[],
  routes: readonly { id: string; type?: string }[],
): Promise<void> {
  const kindById = new Map(routes.map((r) => [r.id, r.type]));

  const lastModified = Date.now();

  await Promise.all(
    groups.map(async (group) => {
      const { key, html, rsc, segments } = group;
      const body = await readMaybe(html.fallback?.filePath);
      if (!body) return;

      const kind = kindById.get(html.parentOutputId) ?? "APP_PAGE";

      const value: Record<string, unknown> = {
        kind,
        status: html.fallback.initialStatus,
      };

      if (kind === "APP_ROUTE") {
        // A single, non-derivable body type: keep content-type verbatim.
        value.headers = html.fallback.initialHeaders;
        value.body = body.toString("base64");
      } else {
        value.headers = html.fallback.initialHeaders;
        value.html = body.toString("utf8");
        const rscBody = await readMaybe(rsc?.fallback?.filePath);
        if (rscBody) value.rscData = rscBody.toString("base64");
        // The same two maps the projection ships, from the same reading of the
        // group: the entry a build seeds and the headers a rewrite reseeds it
        // with cannot disagree.
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

      const dest = join(outputRoot, "cache", `${key}.cache.json`);
      await mkdir(dirname(dest), { recursive: true });
      const entry: CacheEntryFile = { lastModified, value };
      await writeFile(dest, JSON.stringify(entry));
    }),
  );
}

// emitFetchEntries seeds the `fetch`/`unstable_cache` entries the build produced
// under <distDir>/cache/fetch-cache, so a deployed app answers from them instead
// of re-hitting every upstream on the first request to each instance. It is a
// rewrite rather than a copy: the file holds the bare cache *value*, since Next
// synthesizes the envelope's lastModified from the mtime and never stores it.
// They land in their own output folder because they upload to a different
// bucket than route entries (see CacheStore.readFetch for why).
//
// lastModified is stamped at build time, not taken from the mtime, to keep the
// tag clock's pruning proof intact: it rests on every entry in a build having
// lastModified >= deployedAt, and .next/cache survives across builds, so a
// restored entry's mtime can long predate this deploy. That restarts a restored
// entry's revalidate window — so one whose window has already elapsed by its
// mtime is dropped rather than resurrected with a clock it did not earn.
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

      // `revalidate: false` is force-cache: no window to elapse, always kept.
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

// The bundle's Lambda handler: the entry table, and the dispatcher wired to the
// launcher's own `require` so the table's relative specifiers resolve from here.
function renderLauncher(
  entries: Record<string, string>,
  primary: string | null,
): string {
  return (
    [
      `const { AsyncLocalStorage } = require('node:async_hooks')`,
      `globalThis.AsyncLocalStorage = AsyncLocalStorage`,
      `process.env.NODE_ENV ||= 'production'`,
      `const ENTRIES = ${JSON.stringify(entries)}`,
      `const PRIMARY = ${JSON.stringify(primary)}`,
      `module.exports = require(${JSON.stringify(`./${dispatchName}`)})({`,
      `  entries: ENTRIES,`,
      `  primary: PRIMARY,`,
      `  load: (specifier) => require(specifier),`,
      `})`,
    ].join("\n") + "\n"
  );
}

// The path a static file is actually served at, which is also how it is keyed
// in the asset hash map: the error pages arrive as extensionless routes.
function servedPathname(pathname: string): string {
  return pathname === "/404" || pathname === "/500"
    ? `${pathname}.html`
    : pathname;
}

// Copies a file and returns the sha256 of its bytes, hashing as it streams so
// neither memory nor a per-file size cap bounds what a build can ship.
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

// collectPublicFiles walks a project's public/ directory and returns each file
// as a servable static output: its URL pathname (public/ maps to the site root)
// and absolute source path. A missing public/ directory yields no files.
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

// Copies one traced asset, skipping a source that is not there — the packer
// already sized those as missing and the caller warns about them once.
async function copyAsset(srcAbs: string, dest: string): Promise<void> {
  let info;
  try {
    info = await lstat(srcAbs);
  } catch {
    return;
  }
  await mkdir(dirname(dest), { recursive: true });
  // Preserve symlinks verbatim: the tracer emits pnpm's node_modules as a
  // forest of links, and dereferencing them collapses package roots into
  // unresolvable stubs. The link targets are copied as their own asset entries.
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
