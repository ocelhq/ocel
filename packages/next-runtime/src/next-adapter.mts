import type { NextAdapter } from "next";
import { PHASE_PRODUCTION_BUILD } from "next/constants.js";
import { existsSync, readFileSync, writeFileSync } from "node:fs";
import {
  copyFile,
  cp,
  mkdir,
  realpath,
  stat,
  writeFile,
} from "node:fs/promises";
import { dirname, join, relative } from "node:path";

const scratchDir = join(process.cwd(), ".ocel/output");

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
    writeFileSync("args.json", JSON.stringify({ args }, null, 2));

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

    const functionRoutes = [
      ...outputs.pages,
      ...outputs.pagesApi,
      ...outputs.appPages,
      ...outputs.appRoutes,
    ];

    // function routes
    await Promise.all(
      functionRoutes.map(async (fnRoute) => {
        const funcDir = join(
          `${scratchDir}/functions`,
          `${fnRoute.pathname === "/" ? "index" : fnRoute.pathname}.func`,
        );

        const handlerRel = relative(repoRoot, fnRoute.filePath);

        for (const [destRel, srcAbs] of Object.entries(fnRoute.assets)) {
          await copyAsset(srcAbs, join(funcDir, destRel));
          await copyAsset(fnRoute.filePath, join(funcDir, handlerRel));
        }

        await writeFile(
          join(funcDir, "config.json"),
          JSON.stringify({
            runtime: "nodejs24.x",
            handler: handlerRel,
            framework: "next",
          }),
        );
      }),
    );

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
  },
} satisfies NextAdapter;

async function copyAsset(srcAbs: string, dest: string) {
  if (!existsSync(srcAbs)) return;
  const real = await realpath(srcAbs);
  const st = await stat(real);
  if (st.isDirectory()) {
    // Package-root markers from the tracer — copy only package.json,
    // not the whole tree. The specific files are separate asset entries.
    const pkg = join(real, "package.json");
    if (existsSync(pkg)) {
      const pkgDest = join(dest, "package.json");
      await mkdir(dirname(pkgDest), { recursive: true });
      await copyFile(pkg, pkgDest);
    }
    return;
  }
  await mkdir(dirname(dest), { recursive: true });
  await copyFile(real, dest);
}

export default adapter;
