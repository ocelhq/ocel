import type { NextAdapter } from "next";
import { PHASE_PRODUCTION_BUILD } from "next/constants.js";
import { existsSync, readFileSync, writeFileSync } from "node:fs";
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
import { dirname, join, relative, sep } from "node:path";

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

    await Promise.all(
      functionRoutes.map(async (fnRoute) => {
        const funcDir = join(
          `${scratchDir}/functions`,
          `${fnRoute.pathname === "/" ? "index" : fnRoute.pathname}.func`,
        );

        const handlerRel = relative(repoRoot, fnRoute.filePath);

        for (const [destRel, srcAbs] of Object.entries(fnRoute.assets)) {
          await copyAsset(srcAbs, join(funcDir, destRel));
        }
        await copyAsset(fnRoute.filePath, join(funcDir, handlerRel));

        const launcherRel = join(appRel, launcherName);
        await writeFile(
          join(funcDir, launcherRel),
          renderLauncher(relative(projectDir, fnRoute.filePath)),
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
            id: fnRoute.id,
          }),
        );
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

    // pre-renders
    const cacheMap = new Map();
    for (const p of outputs.prerenders) {
      console.log("Prerendered:", p.pathname);

      const parentId = p.parentOutputId;

      if (!cacheMap.get(parentId)) {
        const parent =
          p.id === parentId
            ? p
            : outputs.prerenders.find((p) => p.id === parentId);

        const fallback = parent?.fallback?.filePath;
        const kind = fallback?.endsWith(".body")
          ? "route"
          : fallback?.endsWith(".rsc")
            ? "rsc"
            : "app";

        cacheMap.set(parentId, {
          kind,
          segments: {},
        });
      }

      const e = { ...cacheMap.get(parentId) };
      const f = p.fallback?.filePath;
      const base = f?.replace(/\.(html|rsc|body)$/, "");

      if (!f) continue;

      const contents = existsSync(f) ? readFileSync(f).toString() : undefined;
      const read = (fl: string) =>
        existsSync(fl) ? readFileSync(fl).toString() : undefined;

      if (f.endsWith(".html")) e.html = contents;
      else if (f.endsWith(".body")) e.body = contents;
      else if (f.endsWith(".json")) e.json = contents;
      else if (p.pathname.includes(".segments/")) {
        const seg = p.pathname.split(".segments/")[1]; // "_tree.segment.rsc"
        e.segments[seg as any] = contents;
      } else if (f.endsWith(".rsc")) e.rsc = contents;

      if (read(`${base}.meta`)) {
        e.meta = JSON.parse(read(`${base}.meta`)!.toString());
      }

      cacheMap.set(parentId, e);
    }

    // write isr cache manifest
    writeFileSync(
      `${scratchDir}/isr-cache.json`,
      JSON.stringify(Object.fromEntries(cacheMap)),
    );

    const routingManifest = {
      buildId,
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
          .map((o) => [o.pathname, { kind: "lambda", id: o.id }]),
        ...functionRoutes
          .filter((o) => o.runtime === "edge")
          .map((o) => [
            o.pathname,
            { kind: "edge", entryKey: o.edgeRuntime?.entryKey },
          ]),
        ...outputs.staticFiles.map((o) => [o.pathname, { kind: "static" }]),
        ...publicFiles.map((p) => [p.pathname, { kind: "static" }]),

        // TODO: ISR
        ...outputs.prerenders.map((p) => [
          p.pathname,
          {
            kind: "lambda",
            id: p.id,
            parent: p.parentOutputId,
            revalidate: p.fallback?.initialRevalidate,
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
