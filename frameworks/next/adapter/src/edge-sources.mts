import { readFile } from "node:fs/promises";
import { join } from "node:path";
import type { AdapterOutput } from "next";

export type EdgeOutput =
  | AdapterOutput["PAGES"]
  | AdapterOutput["PAGES_API"]
  | AdapterOutput["APP_PAGE"]
  | AdapterOutput["APP_ROUTE"]
  | AdapterOutput["MIDDLEWARE"];

export function edgeEntryOf(output: EdgeOutput): {
  entryKey: string;
  handlerExport: string;
} {
  if (!output.edgeRuntime) {
    throw new Error(
      `ocel: edge output "${output.pathname}" has no edgeRuntime entry — this build cannot be served`,
    );
  }
  return output.edgeRuntime;
}

interface EdgeManifest {
  middleware: Record<string, EdgeManifestFn>;
  functions: Record<string, EdgeManifestFn>;
}

interface EdgeManifestFn {
  name: string;
  assets?: { name: string; filePath: string }[];
}

async function edgeAssetNames(distDir: string): Promise<Set<string> | null> {
  const path = join(distDir, "server", "middleware-manifest.json");
  let manifest: EdgeManifest;
  try {
    manifest = JSON.parse(await readFile(path, "utf8")) as EdgeManifest;
  } catch (error) {
    console.warn(
      `ocel: could not read ${path} (${error instanceof Error ? error.message : String(error)}) — classifying edge assets by file extension instead`,
    );
    return null;
  }

  const names = new Set<string>();
  for (const fn of [
    ...Object.values(manifest.middleware ?? {}),
    ...Object.values(manifest.functions ?? {}),
  ]) {
    for (const asset of fn.assets ?? []) names.add(asset.name);
  }
  return names;
}

interface EdgeParts {
  chunkPathByKey: Map<string, string>;
  wasmPathByName: Map<string, string>;
  assetPathByName: Map<string, string>;
  env: Record<string, string>;
  entryAssets: Map<string, Set<string>>;
  handlerExports: Map<string, string>;
}

export async function classifyEdgeSources(
  distDir: string,
  sources: readonly EdgeOutput[],
): Promise<EdgeParts> {
  const assetNames = await edgeAssetNames(distDir);

  const isMap = (key: string) => key.endsWith(".map");
  const isChunk = (key: string) =>
    !isMap(key) && (assetNames ? !assetNames.has(key) : /\.[cm]?js$/.test(key));
  const isAsset = (key: string) => !isMap(key) && !isChunk(key);

  const chunkPathByKey = new Map<string, string>();
  const wasmPathByName = new Map<string, string>();
  const assetPathByName = new Map<string, string>();
  const env: Record<string, string> = {};
  const entryAssets = new Map<string, Set<string>>();
  const handlerExports = new Map<string, string>();

  for (const source of sources) {
    const { entryKey, handlerExport } = edgeEntryOf(source);
    handlerExports.set(entryKey, handlerExport);

    const assets = entryAssets.get(entryKey) ?? new Set<string>();
    entryAssets.set(entryKey, assets);
    for (const [key, abs] of Object.entries(source.assets)) {
      if (isAsset(key)) {
        assetPathByName.set(key, abs);
        continue;
      }
      if (!isChunk(key)) continue;
      chunkPathByKey.set(key, abs);
      assets.add(key);
    }

    for (const [name, abs] of Object.entries(source.wasmAssets ?? {})) {
      wasmPathByName.set(name, abs);
    }

    for (const [key, value] of Object.entries(source.config.env ?? {})) {
      const seen = env[key];
      if (seen !== undefined && seen !== value) {
        throw new Error(
          `ocel: edge outputs disagree on the value of env "${key}" — the bundle has one env for every entry`,
        );
      }
      env[key] = value;
    }
  }

  return {
    chunkPathByKey,
    wasmPathByName,
    assetPathByName,
    env,
    entryAssets,
    handlerExports,
  };
}

const DATA_CACHE_CALLS: readonly RegExp[] = [
  /\bnext\s*:\s*\{[^{}]*\b(?:revalidate|tags)\s*:/,
  /\(0,\s*[\w$]+\.(?:revalidateTag|revalidatePath|updateTag|unstable_cache)\)\(/,
];

export async function warnEdgeDataCache(
  sources: readonly EdgeOutput[],
  chunkPathByKey: ReadonlyMap<string, string>,
  entryAssets: ReadonlyMap<string, ReadonlySet<string>>,
): Promise<void> {
  const callingChunks = new Set<string>();
  for (const [key, path] of chunkPathByKey) {
    const text = await readFile(path, "utf8");
    if (DATA_CACHE_CALLS.some((call) => call.test(text))) callingChunks.add(key);
  }
  const callingEntries = new Set(
    [...entryAssets]
      .filter(([, chunks]) => [...chunks].some((key) => callingChunks.has(key)))
      .map(([entryKey]) => entryKey),
  );
  const routes = [
    ...new Set(
      sources
        .filter((s) => s.type !== "MIDDLEWARE" && callingEntries.has(edgeEntryOf(s).entryKey))
        .map((s) => s.pathname),
    ),
  ].sort();
  if (routes.length === 0) return;

  console.warn(
    `ocel: edge route(s) ${routes.join(", ")} set next.revalidate or tags on a fetch, or revalidate a tag or path — the edge keeps no data cache, so each request fetches again and a revalidation raised from the edge reaches nothing`,
  );
}
