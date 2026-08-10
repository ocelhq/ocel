import type { EdgeCacheRpc } from "@ocel/next-cache";

import type { ObjectStoreReader } from "./tag-clock";

export interface EdgeWorkers {
  bundleKey: string;
  id: string;
  compatDate: string;
  compatFlags?: string[];
}

interface EdgeBundle {
  version: number;
  mainModule: string;
  shim: string;
  chunks: Record<string, string>;
  wasm?: Record<string, string>;
  assets?: Record<string, string>;
  env?: Record<string, string>;
}

const BUNDLE_VERSION = 2;

export type EdgeInvoker = (
  entryKey: string,
  request: Request,
) => Promise<Response>;

export type EdgeCacheStub = Rpc.Provider<EdgeCacheRpc>;

export interface EdgeCacheBinding {
  rpc: EdgeCacheStub;
  scope: string;
}

export function createEdgeInvoker(
  loader: WorkerLoader,
  workers: EdgeWorkers,
  store: ObjectStoreReader,
  cache?: EdgeCacheBinding,
): EdgeInvoker {
  const load = async (): Promise<WorkerLoaderWorkerCode> => {
    const object = await store.get(workers.bundleKey);
    if (!object) {
      throw new Error(`ocel: no edge bundle at ${workers.bundleKey}`);
    }
    const bundle = JSON.parse(await object.text()) as EdgeBundle;
    if (bundle.version !== BUNDLE_VERSION) {
      throw new Error(
        `ocel: edge bundle at ${workers.bundleKey} is version ${bundle.version}, which this worker cannot read`,
      );
    }

    const modules: Record<string, WorkerLoaderModule> = {
      [bundle.mainModule]: { js: bundle.shim },
    };
    for (const [id, cjs] of Object.entries(bundle.chunks)) modules[id] = { cjs };
    for (const [id, base64] of Object.entries(bundle.wasm ?? {})) {
      modules[id] = { wasm: base64Bytes(base64) };
    }
    for (const [id, base64] of Object.entries(bundle.assets ?? {})) {
      modules[id] = { data: base64Bytes(base64) };
    }

    return {
      compatibilityDate: workers.compatDate,
      compatibilityFlags: workers.compatFlags,
      mainModule: bundle.mainModule,
      modules,
      env: {
        ...(bundle.env ?? {}),
        ...(cache && { OCEL_CACHE_RPC: cache.rpc, OCEL_CACHE_SCOPE: cache.scope }),
      },
    };
  };

  const id = `edge:${JSON.stringify([workers.id, cache?.scope ?? null])}`;

  return (entryKey, request) =>
    loader
      .get(id, load)
      .getEntrypoint<undefined>(undefined, { props: { entryKey } })
      .fetch(request);
}

function base64Bytes(base64: string): ArrayBuffer {
  const binary = atob(base64);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
  return bytes.buffer;
}
