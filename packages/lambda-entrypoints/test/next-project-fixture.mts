import { mkdir, writeFile } from "node:fs/promises";
import { join } from "node:path";

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
}

export async function writeNextProjectFixture(
  projectDir: string,
  configOverrides: Record<string, unknown> = {},
): Promise<NextProjectFixture> {
  await mkdir(join(projectDir, ".next"), { recursive: true });

  const cacheHandlerPath = join(projectDir, "stub-cache-handler.cjs");
  await writeFile(cacheHandlerPath, cacheHandlerModule);

  const config = {
    distDir: ".next",
    cacheHandler: cacheHandlerPath,
    cacheMaxMemorySize: 0,
    experimental: {
      fetchCacheKeyPrefix: "prefix",
      isrFlushToDisk: true,
    },
    ...configOverrides,
  };
  await writeFile(
    join(projectDir, ".next/required-server-files.json"),
    JSON.stringify({ version: 1, config }),
  );
  await writeFile(
    join(projectDir, ".next/prerender-manifest.json"),
    JSON.stringify({ version: 4, routes: {}, preview: { previewModeId } }),
  );

  return { projectDir, cacheHandlerPath };
}
