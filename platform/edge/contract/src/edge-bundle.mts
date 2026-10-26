export const edgeBundleVersion = 3;

export const EDGE_BUNDLE_OBJECT_FILE = "bundle.json";

export interface EdgeBundleEntry {
  chunks: string[];
  handlerExport: string;
}

export interface EdgeBundle {
  version: number;
  entries: Record<string, EdgeBundleEntry>;
  chunks: Record<string, string>;
  wasm?: Record<string, string>;
  assets?: Record<string, string>;
  wasmGlobals?: Record<string, string>;
  assetNames?: Record<string, string>;
  env?: Record<string, string>;
  clientAssetSuffix: string;
}
