import { mkdir, symlink, writeFile } from "node:fs/promises";
import { createRequire } from "node:module";
import { dirname, join } from "node:path";
import type { LauncherCache, LauncherConfig } from "@framework/next-protocol/launcher";

export const previewModeId = "test-preview-id";

const cacheHandlerModule = `const store = new Map();
class StubCacheHandler {
  constructor(opts) {
    this.opts = opts;
    StubCacheHandler.instances.push(this);
  }
  async get(key) {
    return store.get(key) ?? null;
  }
  async set(key, data) {
    store.set(key, { lastModified: Date.now(), value: data });
  }
  async revalidateTag(tags, durations) {
    StubCacheHandler.revalidated.push({ tags, durations });
  }
  resetRequestCache() {}
}
StubCacheHandler.instances = [];
StubCacheHandler.revalidated = [];
StubCacheHandler.store = store;
module.exports = { default: StubCacheHandler, __esModule: true };
`;

export interface NextProjectFixture {
  projectDir: string;
  cacheHandlerPath: string;
  nextConfig: LauncherConfig;
}

export function launcherConfig(cache: Partial<LauncherCache> = {}): LauncherConfig {
  return {
    basePath: "",
    i18n: null,
    trailingSlash: false,
    experimental: {},
    clientAssetSuffix: "",
    distDir: ".next",
    cache: {
      fetchCacheKeyPrefix: "prefix",
      isrFlushToDisk: true,
      cacheMaxMemorySize: 0,
      previewModeId,
      routes: {},
      dynamicRoutes: [],
      ...cache,
    },
  };
}

export function withLauncherConfig(source: string, nextConfig: LauncherConfig): string {
  return `${source}\nmodule.exports.nextConfig = ${JSON.stringify(nextConfig)};\n`;
}

export async function writeNextProjectFixture(
  projectDir: string,
  cache: Partial<LauncherCache> = {},
): Promise<NextProjectFixture> {
  await mkdir(join(projectDir, ".next"), { recursive: true });
  await mkdir(join(projectDir, "node_modules"), { recursive: true });
  const nextDir = dirname(createRequire(import.meta.url).resolve("next/package.json"));
  await symlink(nextDir, join(projectDir, "node_modules", "next"), "dir");

  const cacheHandlerPath = join(projectDir, "stub-cache-handler.cjs");
  await writeFile(cacheHandlerPath, cacheHandlerModule);

  return { projectDir, cacheHandlerPath, nextConfig: launcherConfig(cache) };
}
