import type { AdapterOutput, NextAdapter } from "next";
import { PHASE_PRODUCTION_BUILD } from "next/constants.js";
import { writeFileSync } from "node:fs";
import {
  copyFile,
  cp,
  lstat,
  mkdir,
  readFile,
  readdir,
  readlink,
  rm,
  symlink,
  writeFile,
} from "node:fs/promises";
import { basename, dirname, join, relative, sep } from "node:path";

const launcherName = "__next_launcher.cjs";

// The ocel builder passes this app's own subtree; a bare `next build` outside
// ocel falls back to the flat cwd path.
function resolveOutputRoot(): string {
  return process.env.OCEL_OUTPUT_DIR || join(process.cwd(), ".ocel/output");
}

// Where the membrane layer mounts the bundled cache handlers. Deliberately not
// set through modifyConfig: `next build` would hand these to its own static
// generation workers, which would then try to reach S3 with no credentials, and
// it rewrites any handler path it is given to one relative to the *build*
// machine's distDir — which does not survive the move to /var/task. Patched
// into the built manifest instead (see patchCacheHandlers).
//
// The singular `cacheHandler` is the incremental cache (ISR, prerenders, Pages
// Router); the plural `cacheHandlers` map, keyed by cache kind, is what backs
// the `use cache` directive. They are separate contracts and separate modules.
const cacheHandlerPath = "/opt/ocel/next/cache-handler.cjs";
const useCacheHandlerPaths = {
  default: "/opt/ocel/next/use-cache-default.cjs",
  remote: "/opt/ocel/next/use-cache-remote.cjs",
};

const adapter = {
  name: "ocel-adapter",

  async modifyConfig(config, { phase }) {
    if (phase === PHASE_PRODUCTION_BUILD) {
      return {
        ...config,
        cacheMaxMemorySize: 0,
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

    const routableOutputs = [...allRoutes, ...outputs.prerenders, ...outputs.staticFiles];

    const functionRoutes = allRoutes.filter((r) => r.runtime === "nodejs");
    const skipped = allRoutes.length - functionRoutes.length;
    if (skipped > 0) {
      console.warn(
        `ocel: skipping ${skipped} non-nodejs route(s) — only the nodejs runtime is supported`,
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

    const funcDirFor = (pathname: string) =>
      join(
        outputRoot,
        "functions",
        `${pathname === "/" ? "index" : pathname}.func`,
      );

    // Routes sharing a filePath and config are the same compiled function —
    // e.g. a page and its `.rsc` variant. Emit one real `.func` per group and
    // symlink the rest to it, mirroring the Vercel Build Output API. The parent
    // is the group's shortest pathname: the base route the variants extend, and
    // the id prerenders reference via parentOutputId.
    const groups = new Map<string, typeof functionRoutes>();
    for (const route of functionRoutes) {
      const key = `${route.filePath}\0${JSON.stringify(route.config)}`;
      const members = groups.get(key);
      if (members) members.push(route);
      else groups.set(key, [route]);
    }

    const parentIdByPathname = new Map<string, string>();
    for (const members of groups.values()) {
      members.sort(
        (a, b) =>
          a.pathname.length - b.pathname.length ||
          (a.pathname < b.pathname ? -1 : 1),
      );
      const parentId = members[0]!.id;
      for (const m of members) parentIdByPathname.set(m.pathname, parentId);
    }

    await Promise.all(
      [...groups.values()].map(async (members) => {
        const parent = members[0]!;
        const variants = members.slice(1);
        const funcDir = funcDirFor(parent.pathname);
        const handlerRel = relative(repoRoot, parent.filePath);

        for (const [destRel, srcAbs] of Object.entries(parent.assets)) {
          await copyAsset(srcAbs, join(funcDir, destRel));
        }
        await copyAsset(parent.filePath, join(funcDir, handlerRel));

        const launcherRel = join(appRel, launcherName);
        await writeFile(
          join(funcDir, launcherRel),
          renderLauncher(relative(projectDir, parent.filePath)),
        );

        await writeFile(
          join(funcDir, "config.json"),
          JSON.stringify({
            runtime: "nodejs24.x",
            handler: launcherRel,
            framework: "next",
            // The route's framework-native identity, carried through to
            // ManifestFunction.route_id so the routing layer can key
            // FUNCTION_URLS by it (the Lambda itself keeps an infra-safe name).
            id: parent.id,
            app: appName,
          }),
        );

        // Each variant reuses the parent Lambda: a relative symlink to the
        // sibling parent `.func`, so the CLI's function walk (which skips
        // symlinked `.func` dirs) deploys the parent only.
        for (const variant of variants) {
          const variantDir = funcDirFor(variant.pathname);
          await mkdir(dirname(variantDir), { recursive: true });
          await symlink(relative(dirname(variantDir), funcDir), variantDir);
        }
      }),
    );

    // public/ assets. Next's outputs.staticFiles covers _next/static and the
    // prerendered error pages but never the project's public/ directory, so the
    // adapter copies it verbatim into the static output and makes each file
    // routable — otherwise a request for e.g. /favicon.svg has no dispatch entry
    // and 404s despite the file existing.
    const publicFiles = await collectPublicFiles(projectDir);
    for (const p of publicFiles) {
      const dest = join(outputRoot, "static", p.pathname);
      await mkdir(dirname(dest), { recursive: true });
      await copyFile(p.filePath, dest);
    }

    // static files
    for (const s of outputs.staticFiles) {
      const normalize = (p: string) =>
        ["/404", "/500"].some((i) => p === i) ? `${p}.html` : p;

      const dest = join(outputRoot, "static", normalize(s.pathname));

      await mkdir(dirname(dest), { recursive: true });
      await copyFile(s.filePath, dest);
    }

    // Seed each prerendered route's cache entry from the build output.
    await emitCacheEntries(outputRoot, outputs.prerenders, allRoutes);
    await emitFetchEntries(outputRoot, distDir);

    const routingManifest = {
      buildId,
      appName,
      basePath: config.basePath || "",
      i18n: config.i18n ?? undefined,
      pathnames: [
        ...new Set([
          ...routableOutputs.map((o) => o.pathname),
          ...publicFiles.map((p) => p.pathname),
        ]),
      ],
      routes: routing,

      dispatch: Object.fromEntries([
        ...functionRoutes
          .filter((o) => o.runtime !== "edge")
          .map((o) => [
            o.pathname,
            { kind: "lambda", id: parentIdByPathname.get(o.pathname) ?? o.id },
          ]),
        ...functionRoutes
          .filter((o) => o.runtime === "edge")
          .map((o) => [
            o.pathname,
            { kind: "edge", entryKey: o.edgeRuntime?.entryKey },
          ]),
        ...outputs.staticFiles.map((o) => [o.pathname, { kind: "static" }]),
        ...publicFiles.map((p) => [p.pathname, { kind: "static" }]),

        // A prerendered pathname resolves to a prerender: its cache entry lives
        // in the asset bucket (keyed by build id) and its id is the parent
        // output's function — the base route deployed as a Lambda that
        // regenerates the entry. Spread last so it replaces the plain lambda
        // entry a prerendered function route also produced above.
        //
        // The fallback is projected down to the two freshness windows rather
        // than spread: the shell, the postponed state, and the entry's own
        // status/headers all travel in the cache entry, so carrying build-time
        // copies here would only put a stale second source of truth (and, for
        // postponedState, ~96KB of it per route) in front of every request.
        ...outputs.prerenders.map((p) => {
          const allowQuery = p.config?.allowQuery;
          const tags = cacheTags(p);

          return [
            p.pathname,
            {
              kind: "prerender",
              id: parentIdByPathname.get(p.pathname) ?? p.parentOutputId,
              config: p.config,
              fallback: {
                initialRevalidate: p.fallback?.initialRevalidate,
                initialExpiration: p.fallback?.initialExpiration,
              },
              ...(p.pprChain && { pprChain: p.pprChain }),
              ...(tags.length > 0 && { tags }),
              ...(allowQuery && { allowQuery }),
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
  },
} satisfies NextAdapter;

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

// emitCacheEntries seeds one cache entry per prerendered route from the build's
// own output, so a deployed route serves its prerender instead of re-rendering
// on the first request to every instance. Next surfaces a route's html, RSC and
// PPR-segment variants as separate PRERENDER outputs sharing a groupId — the
// grouping key here — and this recombines each group into the single entry the
// cache handler reads. Routes whose html variant Next did not prerender (a
// blocking fallback) have nothing to seed and are skipped.
async function emitCacheEntries(
  outputRoot: string,
  prerenders: readonly any[],
  routes: readonly { id: string; type?: string }[],
): Promise<void> {
  const kindById = new Map(routes.map((r) => [r.id, r.type]));

  const byGroup = new Map<number, any[]>();
  for (const p of prerenders) {
    const members = byGroup.get(p.groupId);
    if (members) members.push(p);
    else byGroup.set(p.groupId, [p]);
  }

  const lastModified = Date.now();

  await Promise.all(
    [...byGroup.values()].map(async (members) => {
      // A member is a segment or the .rsc payload by its own suffix; the base is
      // the html variant, the one that is neither. Its concrete pathname keys the
      // entry (e.g. /blog/a → blog/a, / → index) — not parentOutputId, which
      // names the shared function under its dynamic pattern.
      const isSegment = (m: any) => segmentPath(m.pathname) !== null;
      const html = members.find(
        (m) => !m.pathname.endsWith(".rsc") && !isSegment(m),
      );
      const rsc = members.find(
        (m) => m.pathname.endsWith(".rsc") && !isSegment(m),
      );
      const body = await readMaybe(html?.fallback?.filePath);
      if (!html || !body) return;

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
        // Each prerender variant (html, .rsc, per-segment) arrives with its own
        // initialHeaders; storing them verbatim per variant is what lets the edge
        // replay exactly what Next would have served — including the segment
        // cache's x-nextjs-postponed: 2 marker, which lives only on the segment
        // variants and is the header the client gates PPR support on.
        value.headers = html.fallback.initialHeaders;
        value.html = body.toString("utf8");
        const rscBody = await readMaybe(rsc?.fallback?.filePath);
        if (rscBody) value.rscData = rscBody.toString("base64");
        if (rsc?.fallback?.initialHeaders) {
          value.rscHeaders = rsc.fallback.initialHeaders;
        }

        const segments: Record<string, string> = {};
        for (const m of members) {
          const sp = segmentPath(m.pathname);
          if (!sp) continue;
          const segBody = await readMaybe(m.fallback?.filePath);
          if (segBody) segments[sp] = segBody.toString("base64");
          // The segment variants' headers are identical across a group, so the
          // first one seen stands for all of them — stored once, not per segment.
          if (!value.segmentHeaders && m.fallback?.initialHeaders) {
            value.segmentHeaders = m.fallback.initialHeaders;
          }
        }
        if (Object.keys(segments).length > 0) value.segmentData = segments;
        if (html.fallback.postponedState !== undefined) {
          value.postponed = html.fallback.postponedState;
        }
      }

      const key =
        html.pathname === "/" ? "index" : html.pathname.replace(/^\//, "");
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

function renderLauncher(moduleRel: string): string {
  const requirePath = "./" + moduleRel.split(sep).join("/");
  return (
    [
      `const { AsyncLocalStorage } = require('node:async_hooks')`,
      `globalThis.AsyncLocalStorage = AsyncLocalStorage`,
      `process.env.NODE_ENV ||= 'production'`,
      `module.exports = require(${JSON.stringify(requirePath)})`,
    ].join("\n") + "\n"
  );
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

async function copyAsset(srcAbs: string, dest: string) {
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
