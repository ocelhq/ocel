import {
  mkdtemp,
  mkdir,
  writeFile,
  readFile,
  readdir,
  stat,
  lstat,
  readlink,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join, relative } from "node:path";
import { afterEach, beforeEach, expect, test, vi } from "vitest";

// onBuildComplete writes everything under process.cwd() (its scratch dir binds
// to the cwd at import time, mirroring how the real builder invokes it inside
// `next build` in the app directory). Each test runs inside a throwaway project:
// it chdirs there and imports the adapter fresh so the scratch dir resolves to
// that project, then restores the cwd afterward.
let originalCwd: string;

beforeEach(() => {
  originalCwd = process.cwd();
});

afterEach(() => {
  process.chdir(originalCwd);
});

async function loadAdapterIn(projectDir: string) {
  process.chdir(projectDir);
  vi.resetModules();
  const mod = await import("../src/next-adapter.mts");
  return mod.default;
}

// A minimal, hermetic build result exercising one nodejs function route plus a
// public/ directory — enough to assert routing/static wiring without depending
// on a real Next build.
async function synthProject() {
  const projectDir = await mkdtemp(join(tmpdir(), "ocel-next-"));

  await mkdir(join(projectDir, "public", "icons"), { recursive: true });
  await writeFile(join(projectDir, "public", "next.svg"), "<svg/>");
  await writeFile(join(projectDir, "public", "icons", "logo.png"), "x");

  const handler = join(
    projectDir,
    ".next/server/app/api/documents/route.js",
  );
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
    repoRoot: projectDir,
    distDir: join(projectDir, ".next"),
    config: { basePath: "" },
    nextVersion: "16.2.10",
    buildId: "test-build",
  };

  return { projectDir, args };
}

async function exists(p: string): Promise<boolean> {
  try {
    await stat(p);
    return true;
  } catch {
    return false;
  }
}

// A build result where routes come in base + `.rsc` pairs that share the same
// filePath (and config, assets): the root page (/ and /index.rsc), plus an app
// route (/api/documents and /api/documents.rsc). The root page is also
// prerendered, so its dispatch entry is emitted by the prerenders loop.
async function synthDedupProject() {
  const projectDir = await mkdtemp(join(tmpdir(), "ocel-next-dedup-"));

  const pageHandler = join(projectDir, ".next/server/app/page.js");
  const routeHandler = join(
    projectDir,
    ".next/server/app/api/documents/route.js",
  );
  const shared = join(projectDir, ".next/server/chunks/shared.js");
  for (const f of [pageHandler, routeHandler, shared]) {
    await mkdir(dirname(f), { recursive: true });
    await writeFile(f, "module.exports = () => {}");
  }
  const pageAssets = { "chunks/shared.js": shared };

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
      appPages: [
        {
          pathname: "/index.rsc",
          id: "/index.rsc",
          assets: pageAssets,
          runtime: "nodejs",
          filePath: pageHandler,
          config: {},
          type: "APP_PAGE",
        },
        {
          pathname: "/",
          id: "/",
          assets: pageAssets,
          runtime: "nodejs",
          filePath: pageHandler,
          config: {},
          type: "APP_PAGE",
        },
      ],
      appRoutes: [
        {
          pathname: "/api/documents",
          id: "/api/documents",
          assets: {},
          runtime: "nodejs",
          filePath: routeHandler,
          config: {},
          type: "APP_ROUTE",
        },
        {
          pathname: "/api/documents.rsc",
          id: "/api/documents.rsc",
          assets: {},
          runtime: "nodejs",
          filePath: routeHandler,
          config: {},
          type: "APP_ROUTE",
        },
      ],
      staticFiles: [],
      prerenders: [
        {
          pathname: "/",
          id: "/",
          parentOutputId: "/",
          fallback: { filePath: join(projectDir, "index.html") },
        },
        {
          pathname: "/index.rsc",
          id: "/index.rsc",
          parentOutputId: "/",
          fallback: { filePath: join(projectDir, "index.rsc") },
        },
      ],
    },
    projectDir,
    repoRoot: projectDir,
    distDir: join(projectDir, ".next"),
    config: { basePath: "" },
    nextVersion: "16.2.10",
    buildId: "test-build",
  };

  return { projectDir, args };
}

async function isSymlink(p: string): Promise<boolean> {
  try {
    return (await lstat(p)).isSymbolicLink();
  } catch {
    return false;
  }
}

function functionsDir(projectDir: string): string {
  return join(projectDir, ".ocel/output/functions");
}

// Partitions every `.func` under functions/ into the real directories (one per
// deployed Lambda) and the symlinks (reused variants), each relative to
// functions/ so paths read like "index.func" / "api/documents.func".
async function partitionFuncDirs(projectDir: string) {
  const root = functionsDir(projectDir);
  const real: string[] = [];
  const links: string[] = [];
  const walk = async (dir: string) => {
    for (const entry of await readdir(dir, { withFileTypes: true })) {
      const abs = join(dir, entry.name);
      if (entry.isSymbolicLink() && entry.name.endsWith(".func")) {
        links.push(relative(root, abs));
      } else if (entry.isDirectory() && entry.name.endsWith(".func")) {
        real.push(relative(root, abs));
      } else if (entry.isDirectory()) {
        await walk(abs);
      }
    }
  };
  await walk(root);
  return { real: real.sort(), links: links.sort() };
}

async function readManifest(projectDir: string) {
  return JSON.parse(
    await readFile(
      join(projectDir, ".ocel/output/routing-manifest.json"),
      "utf8",
    ),
  );
}

test("creates one real .func per shared filePath and symlinks the variants", async () => {
  const { projectDir, args } = await synthDedupProject();
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const fns = functionsDir(projectDir);
  // Exactly one real .func per group — no accidental extra function.
  const { real, links } = await partitionFuncDirs(projectDir);
  expect(real).toEqual(["api/documents.func", "index.func"]);
  expect(links).toEqual(["api/documents.rsc.func", "index.rsc.func"]);
  // Variants symlink to their sibling parent, relatively.
  expect(await readlink(join(fns, "index.rsc.func"))).toBe("index.func");
  expect(await readlink(join(fns, "api/documents.rsc.func"))).toBe(
    "documents.func",
  );
});

test("variant .func symlinks carry no copied assets or config of their own", async () => {
  const { projectDir, args } = await synthDedupProject();
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const fns = functionsDir(projectDir);
  // The variant is purely a symlink — no directory of its own to copy into.
  expect(await isSymlink(join(fns, "index.rsc.func"))).toBe(true);
  // The parent owns the sole config.json, carrying the parent's id; reading it
  // through the variant symlink resolves to that same file.
  const parentCfg = JSON.parse(
    await readFile(join(fns, "index.func/config.json"), "utf8"),
  );
  const viaSymlink = JSON.parse(
    await readFile(join(fns, "index.rsc.func/config.json"), "utf8"),
  );
  expect(parentCfg.id).toBe("/");
  expect(viaSymlink).toEqual(parentCfg);
});

test("repoints variant dispatch ids to the parent function", async () => {
  const { projectDir, args } = await synthDedupProject();
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const manifest = await readManifest(projectDir);
  // Every route stays a distinct dispatch key.
  expect(manifest.dispatch["/"].id).toBe("/");
  expect(manifest.dispatch["/index.rsc"].id).toBe("/");
  expect(manifest.dispatch["/api/documents"].id).toBe("/api/documents");
  expect(manifest.dispatch["/api/documents.rsc"].id).toBe("/api/documents");
  // Both variants remain routable.
  expect(manifest.pathnames).toContain("/index.rsc");
  expect(manifest.pathnames).toContain("/api/documents.rsc");
});

test("copies public/ files into the static output, recursively", async () => {
  const { projectDir, args } = await synthProject();
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const staticDir = join(projectDir, ".ocel/output/static");
  expect(await exists(join(staticDir, "next.svg"))).toBe(true);
  expect(await exists(join(staticDir, "icons/logo.png"))).toBe(true);
});

test("enumerates public/ files as static in the routing manifest", async () => {
  const { projectDir, args } = await synthProject();
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const manifest = JSON.parse(
    await readFile(
      join(projectDir, ".ocel/output/routing-manifest.json"),
      "utf8",
    ),
  );

  expect(manifest.pathnames).toContain("/next.svg");
  expect(manifest.pathnames).toContain("/icons/logo.png");
  expect(manifest.dispatch["/next.svg"]).toEqual({ kind: "static" });
  expect(manifest.dispatch["/icons/logo.png"]).toEqual({ kind: "static" });
});

test("writes each function's route id into its config.json", async () => {
  const { projectDir, args } = await synthProject();
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const config = JSON.parse(
    await readFile(
      join(
        projectDir,
        ".ocel/output/functions/api/documents.func/config.json",
      ),
      "utf8",
    ),
  );

  expect(config.id).toBe("/api/documents");
  expect(config.framework).toBe("next");
});
