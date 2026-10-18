import { env } from "cloudflare:test";
import { type EdgeBundleEntry, edgeBundleVersion } from "@platform/edge-contract/edge-bundle";

declare module "cloudflare:test" {
  interface ProvidedEnv {
    LOADER: WorkerLoader;
  }
}

export function chunkFor(entryKey: string, handler: string): string {
  return `globalThis._ENTRIES ??= {}
globalThis._ENTRIES[${JSON.stringify(entryKey)}] = { handler: ${handler} }
`;
}

function base64Of(bytes: Uint8Array): string {
  return btoa(String.fromCharCode(...bytes));
}

export interface BundleOptions {
  chunkSource?: (entryKey: string, handler: string) => string;
  assets?: Record<string, Uint8Array>;
  env?: Record<string, string>;
  version?: number;
}

export function edgeBundle(handlers: Record<string, string>, options: BundleOptions = {}): string {
  const chunkSource = options.chunkSource ?? chunkFor;
  const chunks: Record<string, string> = {};
  const entries: Record<string, EdgeBundleEntry> = {};
  let next = 0;
  for (const [entryKey, handler] of Object.entries(handlers)) {
    const id = `c/${next++}.js`;
    chunks[id] = chunkSource(entryKey, handler);
    entries[entryKey] = { chunks: [id], handlerExport: "handler" };
  }

  const assets: Record<string, string> = {};
  const assetIdByName: Record<string, string> = {};
  let nextAsset = 0;
  for (const [name, bytes] of Object.entries(options.assets ?? {})) {
    const id = `a/${nextAsset++}.bin`;
    assets[id] = base64Of(bytes);
    assetIdByName[name] = id;
  }

  return JSON.stringify({
    version: options.version ?? edgeBundleVersion,
    entries,
    chunks,
    wasm: {},
    assets,
    assetNames: assetIdByName,
    env: options.env ?? { __NEXT_BUILD_ID: "t" },
    clientAssetSuffix: "",
  });
}

export interface EdgeInvocation {
  entryKey: string;
  url: string;
}

export function countingLoader(inner: WorkerLoader = env.LOADER): {
  loader: WorkerLoader;
  invocations: EdgeInvocation[];
} {
  const invocations: EdgeInvocation[] = [];
  const loader = {
    get: (id: string, code: () => Promise<WorkerLoaderWorkerCode>) => {
      const worker = inner.get(id, code);
      return {
        getEntrypoint: (name: undefined, options: { props: { entryKey: string } }) => {
          const entrypoint = worker.getEntrypoint(name, options);
          return {
            fetch: (request: Request) => {
              invocations.push({ entryKey: options.props.entryKey, url: request.url });
              return entrypoint.fetch(request);
            },
          };
        },
      };
    },
  } as unknown as WorkerLoader;
  return { loader, invocations };
}
