// Next's edge outputs — middleware and every `runtime: 'edge'` route — run as a
// Cloudflare dynamic worker rather than an uploaded script: the Deployment's
// edge bundle is one R2 object, and the loader compiles it on a cold isolate,
// keyed by the bundle's content hash. Nothing is uploaded at deploy time, which
// is what keeps a rollback a pointer flip (ADR 0002).

import type { ObjectStoreReader } from "./tag-clock";

// The Deployment record's edge slot, written by the host at deploy time.
export interface EdgeWorkers {
  bundleKey: string;
  // Hashes the bundle bytes together with the compat settings below: the
  // loader's contract is "same id, same code", and compat is part of the code
  // it compiles.
  id: string;
  compatDate: string;
  compatFlags?: string[];
}

// The bundle the adapter emits. Chunk and wasm ids are opaque module names it
// assigns; the shim imports them by those names.
interface EdgeBundle {
  version: number;
  mainModule: string;
  shim: string;
  chunks: Record<string, string>;
  wasm?: Record<string, string>;
  env?: Record<string, string>;
}

// The bundle layout this worker knows how to compile. The worker script is
// frozen at deploy time and outlives the deployments it serves (ADR 0002), so it
// can legitimately be handed a bundle a later adapter wrote; a shape it cannot
// read is a 500, not a best-effort load.
const BUNDLE_VERSION = 1;

// Invokes one entry of the Deployment's edge bundle. Throws on any failure —
// callers turn that into a 500 rather than routing on as though the entry had
// never run.
export type EdgeInvoker = (
  entryKey: string,
  request: Request,
) => Promise<Response>;

export function createEdgeInvoker(
  loader: WorkerLoader,
  workers: EdgeWorkers,
  store: ObjectStoreReader,
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
    for (const [id, js] of Object.entries(bundle.chunks)) modules[id] = { js };
    for (const [id, base64] of Object.entries(bundle.wasm ?? {})) {
      modules[id] = { wasm: wasmBytes(base64) };
    }

    return {
      compatibilityDate: workers.compatDate,
      compatibilityFlags: workers.compatFlags,
      mainModule: bundle.mainModule,
      modules,
      // The bundle's own env and nothing else: the chunks read it off
      // process.env at module scope, and no binding is reachable from an edge
      // entry while edge ISR stays out of scope (bd ocelhq-b7l).
      env: bundle.env ?? {},
    };
  };

  // The entry key travels as ctx.props, never a header: the request reaches the
  // entry byte-exact, and no client can name an entry its URL never routed to.
  return (entryKey, request) =>
    loader
      .get(`edge:${workers.id}`, load)
      .getEntrypoint<undefined>(undefined, { props: { entryKey } })
      .fetch(request);
}

function wasmBytes(base64: string): ArrayBuffer {
  const binary = atob(base64);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
  return bytes.buffer;
}
