import { mkdir, mkdtemp, symlink, writeFile } from "node:fs/promises";
import { createRequire } from "node:module";
import { tmpdir } from "node:os";
import { dirname, join, relative } from "node:path";
import { nextIncrementalCacheModule } from "@framework/next-protocol/launcher";
import { afterEach, expect, test, vi } from "vitest";
import { defaultImages, writePrerenderManifest } from "./fixtures.mts";

const originalCwd = process.cwd();

afterEach(() => {
  process.chdir(originalCwd);
  vi.restoreAllMocks();
});

async function writeStubNext(projectDir: string): Promise<void> {
  const nextDir = join(projectDir, "node_modules/next");
  const cacheDir = join(nextDir, "dist/server/lib/incremental-cache");
  await mkdir(cacheDir, { recursive: true });
  await writeFile(join(nextDir, "package.json"), JSON.stringify({ name: "next" }));
  await writeFile(
    join(cacheDir, "index.js"),
    'require("./file-system-cache.js");\nexports.IncrementalCache = class {};\n',
  );
  await writeFile(join(cacheDir, "file-system-cache.js"), "module.exports = {};\n");

  const nft = dirname(createRequire(import.meta.url).resolve("next/dist/compiled/@vercel/nft"));
  await mkdir(join(nextDir, "dist/compiled/@vercel"), { recursive: true });
  await symlink(nft, join(nextDir, "dist/compiled/@vercel/nft"));
}

async function synthApp() {
  const repoRoot = await mkdtemp(join(tmpdir(), "ocel-next-requires-"));
  const projectDir = join(repoRoot, "app");
  await writePrerenderManifest(join(projectDir, ".next"));
  await writeStubNext(projectDir);

  const handler = join(projectDir, ".next/server/app/api/documents/route.js");
  await mkdir(dirname(handler), { recursive: true });
  await writeFile(handler, "module.exports = () => {}");

  const args = {
    routing: {
      beforeMiddleware: [],
      beforeFiles: [],
      afterFiles: [],
      dynamicRoutes: [],
      onMatch: [],
      fallback: [],
    },
    outputs: {
      pages: [],
      pagesApi: [],
      appPages: [],
      appRoutes: [
        {
          pathname: "/api/documents",
          id: "/api/documents",
          assets: {},
          runtime: "nodejs",
          filePath: handler,
          type: "APP_ROUTE",
        },
      ],
      staticFiles: [],
      prerenders: [],
    },
    projectDir,
    repoRoot,
    distDir: join(projectDir, ".next"),
    config: { basePath: "", images: { ...defaultImages, unoptimized: true } },
    nextVersion: "16.2.10",
    buildId: "test-build",
  };
  return { projectDir, args };
}

test("a function bundle carries the Next modules its runtime requires at boot", async () => {
  const { projectDir, args } = await synthApp();
  process.chdir(projectDir);
  vi.spyOn(console, "warn").mockImplementation(() => {});
  vi.resetModules();
  const adapter = (await import("../src/next-adapter.mts")).default;

  await adapter.onBuildComplete!(args as any);

  const funcDir = join(projectDir, ".ocel/output/functions/bundle-0.func");
  const launcherDir = join(funcDir, "app");
  const resolved = createRequire(join(launcherDir, "package.json")).resolve(
    nextIncrementalCacheModule,
  );
  expect(relative(funcDir, resolved)).toBe(
    "app/node_modules/next/dist/server/lib/incremental-cache/index.js",
  );
  expect(createRequire(resolved).resolve("./file-system-cache.js").startsWith(`${funcDir}/`)).toBe(
    true,
  );
});
