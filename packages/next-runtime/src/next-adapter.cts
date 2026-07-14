import type { NextAdapter } from "next";
import { PHASE_PRODUCTION_BUILD } from "next/constants";
import { existsSync, readFileSync, writeFileSync } from "node:fs";
import { copyFile, cp, mkdir, realpath, stat } from "node:fs/promises";
import { dirname, join } from "node:path";

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

        for (const [destRel, srcAbs] of Object.entries(fnRoute.assets)) {
          await copyAsset(srcAbs, join(funcDir, destRel));
        }
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
  // Dereference: pnpm entries are symlinks to the real package dir/file.
  const real = await realpath(srcAbs);
  const st = await stat(real); // statSync already follows symlinks; realpath guards nested ones
  mkdir(dirname(dest), { recursive: true });
  if (st.isDirectory()) {
    await cp(real, dest, { recursive: true, dereference: true });
  } else {
    await copyFile(real, dest);
  }
}

export default adapter;
