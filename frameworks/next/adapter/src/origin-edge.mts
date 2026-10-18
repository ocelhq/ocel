import { join } from "node:path";
import { classifyEdgeSources, type EdgeOutput } from "./edge-sources.mjs";
import { middlewareEntryKey } from "./launcher.mjs";
import { stableStringify } from "./stable-json.mjs";

const originEdgeDirName = "__ocel_edge";

export const originEdgeRuntimeName = "__ocel_edge_entry.cjs";

export interface OriginEdgePlan {
  seedAssets: Record<string, string>;
  files: Record<string, string>;
  entries: Record<string, string>;
}

export async function planOriginEdgeEntries(
  distDir: string,
  sources: readonly EdgeOutput[],
  appRel: string,
  clientAssetSuffix: string,
  launcherKeyOf: (edgeEntryKey: string) => string,
): Promise<OriginEdgePlan | null> {
  if (sources.length === 0) return null;

  const parts = await classifyEdgeSources(distDir, sources);

  const seedAssets: Record<string, string> = {};
  const seed = (kind: string, name: string, abs: string): string => {
    const rel = [originEdgeDirName, kind, name].join("/");
    seedAssets[join(appRel, ...rel.split("/"))] = abs;
    return `./${rel}`;
  };

  const chunkRelByKey = new Map<string, string>();
  for (const [key, abs] of parts.chunkPathByKey) {
    chunkRelByKey.set(key, seed("c", key, abs));
  }
  const assets: Record<string, string> = {};
  for (const [name, abs] of parts.assetPathByName) {
    assets[name] = seed("a", name, abs);
  }
  const wasm: Record<string, string> = {};
  for (const [name, abs] of parts.wasmPathByName) {
    wasm[name] = seed("w", `${name}.wasm`, abs);
  }

  const files: Record<string, string> = {
    [`${originEdgeDirName}/package.json`]: JSON.stringify({
      type: "commonjs",
    }),
  };
  const entries: Record<string, string> = {};
  let index = 0;
  for (const edgeEntryKey of [...parts.entryAssets.keys()].sort()) {
    const launcherKey = launcherKeyOf(edgeEntryKey);
    const wrapper = `__ocel_edge_${index++}.cjs`;
    files[wrapper] = renderOriginEdgeWrapper({
      kind: launcherKey === middlewareEntryKey ? "middleware" : "route",
      entryKey: edgeEntryKey,
      handlerExport: parts.handlerExports.get(edgeEntryKey)!,
      chunks: [...parts.entryAssets.get(edgeEntryKey)!].map((key) => chunkRelByKey.get(key)!),
      assets,
      wasm,
      env: parts.env,
      clientAssetSuffix,
    });
    entries[launcherKey] = `./${wrapper}`;
  }

  return { seedAssets, files, entries };
}

function renderOriginEdgeWrapper(spec: {
  kind: string;
  entryKey: string;
  handlerExport: string;
  chunks: string[];
  assets: Record<string, string>;
  wasm: Record<string, string>;
  env: Record<string, string>;
  clientAssetSuffix: string;
}): string {
  return `${[
    `module.exports = require(${JSON.stringify(`./${originEdgeRuntimeName}`)}).entry(`,
    `  __dirname,`,
    `  ${stableStringify(spec)},`,
    `)`,
  ].join("\n")}\n`;
}
