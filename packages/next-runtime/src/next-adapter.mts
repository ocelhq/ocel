import type { NextAdapter } from "next";
import { PHASE_PRODUCTION_BUILD } from "next/constants.js";
import { writeFileSync } from "node:fs";
import {
  copyFile,
  cp,
  lstat,
  mkdir,
  readdir,
  readlink,
  rm,
  symlink,
  writeFile,
} from "node:fs/promises";
import { basename, dirname, extname, join, relative, sep } from "node:path";

const scratchDir = join(process.cwd(), ".ocel/output");
const launcherName = "__next_launcher.cjs";

const adapter = {
  name: "ocel-adapter",

  async modifyConfig(config, { phase }) {
    if (phase === PHASE_PRODUCTION_BUILD) {
      return {
        ...config,

        // TODO: cache handlers
        // cacheHandler: {},
        // cacheHandlers: {},
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

    const routableOutputs = [...allRoutes, ...outputs.staticFiles];

    const functionRoutes = allRoutes.filter((r) => r.runtime === "nodejs");
    const skipped = allRoutes.length - functionRoutes.length;
    if (skipped > 0) {
      console.warn(
        `ocel: skipping ${skipped} non-nodejs route(s) — only the nodejs runtime is supported`,
      );
    }

    const appRel = relative(repoRoot, projectDir);

    const funcDirFor = (pathname: string) =>
      join(
        `${scratchDir}/functions`,
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
      const dest = join("./.ocel/output/static", p.pathname);
      await mkdir(dirname(dest), { recursive: true });
      await copyFile(p.filePath, dest);
    }

    // static files
    for (const s of outputs.staticFiles) {
      const normalize = (p: string) =>
        ["/404", "/500"].some((i) => p === i) ? `${p}.html` : p;

      const dest = join("./.ocel/output/static", normalize(s.pathname));

      await mkdir(dirname(dest), { recursive: true });
      await copyFile(s.filePath, dest);
    }

    // pre-renders. Each PRERENDER output is one servable variant; emit a
    // Vercel-style config + fallback pair colocated beside its `.func`,
    // mirroring the Build Output API so the CLI can crawl them by glob. The
    // config carries Next's raw fields verbatim, with fallback.filePath
    // rewritten from the absolute build path to the sibling fallback filename
    // the CLI uploads alongside it. The base is the full pathname (retaining
    // any .rsc/.html suffix) so an html variant and its .rsc variant never
    // collide on one config name.
    await Promise.all(
      outputs.prerenders.map(async (p) => {
        const base = p.pathname === "/" ? "index" : p.pathname.replace(/^\//, "");
        const configPath = join(
          `${scratchDir}/functions`,
          `${base}.prerender-config.json`,
        );
        await mkdir(dirname(configPath), { recursive: true });

        const config: Record<string, unknown> = { ...p };
        const src = p.fallback?.filePath;
        if (src) {
          const fallbackName = `${basename(base)}.prerender-fallback${extname(src)}`;
          await copyAsset(src, join(dirname(configPath), fallbackName));
          config.fallback = { ...p.fallback, filePath: fallbackName };
        }

        await writeFile(configPath, JSON.stringify(config));
      }),
    );

    // The ocel app name keys this app's assets in the account-global bucket
    // (<env>/<project>/<appName>/<buildId>/…). The ocel builder passes it via
    // OCEL_APP_NAME; falling back to the project dir name keeps a bare
    // `next build` self-consistent.
    const appName = process.env.OCEL_APP_NAME || basename(projectDir);

    const routingManifest = {
      buildId,
      appName,
      basePath: config.basePath || "",
      i18n: config.i18n ?? undefined,
      pathnames: [
        ...routableOutputs.map((o) => o.pathname),
        ...publicFiles.map((p) => p.pathname),
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

        // A prerendered pathname resolves to a prerender: its config + fallback
        // live in the asset bucket (keyed by build id) and its id is the parent
        // output's function — the base route deployed as a Lambda that
        // regenerates the entry. Spread last so it replaces the plain lambda
        // entry a prerendered function route also produced above.
        ...outputs.prerenders.map((p) => [
          p.pathname,
          {
            kind: "prerender",
            id: parentIdByPathname.get(p.pathname) ?? p.parentOutputId,
          },
        ]),
      ]),
    };

    writeFileSync(
      `${scratchDir}/routing-manifest.json`,
      JSON.stringify(routingManifest),
    );
  },
} satisfies NextAdapter;

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
