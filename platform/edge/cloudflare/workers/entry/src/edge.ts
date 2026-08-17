import type { EdgeCacheRpc } from "@framework/next-cache";

export interface EdgeStoredObject {
  text(): Promise<string>;
  arrayBuffer(): Promise<ArrayBuffer>;
}

export interface EdgeObjectStore {
  get(key: string): Promise<EdgeStoredObject | null>;
}

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

const SEALED_FILE = "sealed.bin";

const NONCE_BYTES = 12;

const VARIABLE_PREFIX = "OCEL_VAR_";

export interface EdgeVariables {
  env?: Record<string, string>;
  envelope?: string;
  valueFingerprint?: string;
}

export type EdgeEntryKind = "page" | "middleware";

export type EdgeInvoker = (
  entryKey: string,
  request: Request,
  kind?: EdgeEntryKind,
) => Promise<Response>;

export type EdgeCacheStub = Rpc.Provider<EdgeCacheRpc>;

export interface EdgeCacheBinding {
  rpc: EdgeCacheStub;
  scope: string;
}

export function createEdgeInvoker(
  loader: WorkerLoader,
  workers: EdgeWorkers,
  store: EdgeObjectStore,
  cache?: EdgeCacheBinding,
  variables?: EdgeVariables,
): EdgeInvoker {
  const envelope = variables?.envelope;
  const sealedKey = siblingKey(workers.bundleKey, SEALED_FILE);

  const load = async (): Promise<WorkerLoaderWorkerCode> => {
    const [object, sealed] = await Promise.all([
      store.get(workers.bundleKey),
      envelope ? store.get(sealedKey) : null,
    ]);
    if (!object) {
      throw new Error(`ocel: no edge bundle at ${workers.bundleKey}`);
    }
    if (envelope && !sealed) {
      throw new Error(`ocel: no sealed edge variables at ${sealedKey}`);
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
        ...(variables?.env ?? {}),
        ...(envelope && sealed
          ? prefixed(await unseal(envelope, await sealed.arrayBuffer()))
          : {}),
        ...(cache && { OCEL_CACHE_RPC: cache.rpc, OCEL_CACHE_SCOPE: cache.scope }),
      },
    };
  };

  const idFor = (kind: EdgeEntryKind) =>
    `edge:${JSON.stringify([
      workers.id,
      cache?.scope ?? null,
      variables?.valueFingerprint ?? null,
      kind,
    ])}`;

  return (entryKey, request, kind = "page") =>
    loader
      .get(idFor(kind), load)
      .getEntrypoint<undefined>(undefined, { props: { entryKey } })
      .fetch(request);
}

function siblingKey(key: string, name: string): string {
  return key.slice(0, key.lastIndexOf("/") + 1) + name;
}

async function unseal(
  envelope: string,
  sealed: ArrayBuffer,
): Promise<Record<string, string>> {
  const key = await crypto.subtle.importKey(
    "raw",
    base64Bytes(envelope),
    { name: "AES-GCM" },
    false,
    ["decrypt"],
  );
  const bytes = new Uint8Array(sealed);
  const payload = await crypto.subtle.decrypt(
    { name: "AES-GCM", iv: bytes.subarray(0, NONCE_BYTES) },
    key,
    bytes.subarray(NONCE_BYTES),
  );
  try {
    return JSON.parse(new TextDecoder().decode(payload)) as Record<string, string>;
  } catch {
    throw new Error("ocel: sealed edge variables are not JSON");
  }
}

function prefixed(values: Record<string, string>): Record<string, string> {
  const env: Record<string, string> = {};
  for (const [key, value] of Object.entries(values)) {
    env[VARIABLE_PREFIX + key] = value;
  }
  return env;
}

function base64Bytes(base64: string): ArrayBuffer {
  const binary = atob(base64);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
  return bytes.buffer;
}
