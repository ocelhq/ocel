import { createHash } from "node:crypto";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import {
  type EdgeBundle,
  type EdgeBundleEntry,
  edgeBundleVersion,
} from "@platform/edge-contract/edge-bundle";
import { classifyEdgeSources, type EdgeOutput, warnEdgeDataCache } from "./edge-sources.mjs";
import { stableStringify } from "./stable-json.mjs";

async function moduleIds(
  pathByKey: Map<string, string>,
  idOf: (index: number) => string,
  encode: (bytes: Buffer) => string,
): Promise<{ idByKey: Map<string, string>; modules: Record<string, string> }> {
  const idByKey = new Map<string, string>();
  const idByHash = new Map<string, string>();
  const modules: Record<string, string> = {};
  for (const key of [...pathByKey.keys()].sort()) {
    const bytes = await readFile(pathByKey.get(key)!);
    const hash = createHash("sha256").update(bytes).digest("hex");
    let id = idByHash.get(hash);
    if (!id) {
      id = idOf(idByHash.size);
      idByHash.set(hash, id);
      modules[id] = encode(bytes);
    }
    idByKey.set(key, id);
  }
  return { idByKey, modules };
}

export async function emitEdgeBundle(
  outputRoot: string,
  distDir: string,
  clientAssetSuffix: string,
  sources: readonly EdgeOutput[],
): Promise<void> {
  if (sources.length === 0) return;

  const { chunkPathByKey, wasmPathByName, assetPathByName, env, entryAssets, handlerExports } =
    await classifyEdgeSources(distDir, sources);

  await warnEdgeDataCache(sources, chunkPathByKey, entryAssets);

  const { idByKey: chunkIdByKey, modules: chunks } = await moduleIds(
    chunkPathByKey,
    (n) => `c/${n}.js`,
    (bytes) => bytes.toString("utf8"),
  );
  const { idByKey: wasmIdByName, modules: wasmModules } = await moduleIds(
    wasmPathByName,
    (n) => `w/${n}.wasm`,
    (bytes) => bytes.toString("base64"),
  );
  const { idByKey: assetIdByName, modules: assetModules } = await moduleIds(
    assetPathByName,
    (n) => `a/${n}.bin`,
    (bytes) => bytes.toString("base64"),
  );

  const entries: Record<string, EdgeBundleEntry> = {};
  for (const [entryKey, keys] of entryAssets) {
    entries[entryKey] = {
      chunks: [...new Set([...keys].map((k) => chunkIdByKey.get(k)!))],
      handlerExport: handlerExports.get(entryKey)!,
    };
  }

  const bundle: EdgeBundle = {
    version: edgeBundleVersion,
    entries,
    chunks,
    wasm: wasmModules,
    assets: assetModules,
    wasmGlobals: Object.fromEntries(wasmIdByName),
    assetNames: Object.fromEntries(assetIdByName),
    env,
    clientAssetSuffix,
  };
  const json = stableStringify(bundle);

  const dest = join(outputRoot, "edge", "bundle.json");
  await mkdir(dirname(dest), { recursive: true });
  await writeFile(dest, json);

  const mb = (Buffer.byteLength(json) / 1024 / 1024).toFixed(1);
  console.log(
    `ocel: edge bundle ${mb} MB, ${Object.keys(chunks).length} chunks, ${Object.keys(assetModules).length} assets, ${Object.keys(entries).length} entries`,
  );
}
