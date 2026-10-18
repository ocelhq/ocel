import { type EdgeBundle, edgeBundleVersion } from "@platform/edge-contract/edge-bundle";
import { renderWorkersShim, WORKERS_SHIM_MODULE } from "./workers-shim";

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

const SEALED_FILE = "sealed.bin";

const NONCE_BYTES = 12;

export interface EdgeVariables {
  scope?: string;
  env?: Record<string, string>;
  envelope?: string;
  envelopeKey?: string;
  releaseFingerprint?: string;
}

const BUNDLE_FILE = "bundle.json";

const BUNDLE_DIR = "edge";

export function ownBundleKey(bundleKey: string, slug: string, app: string): boolean {
  const segments = bundleKey.split("/");
  return (
    segments.length === 6 &&
    segments.every((segment) => segment !== "" && segment !== "." && segment !== "..") &&
    segments[1] === slug &&
    segments[2] === app &&
    segments[4] === BUNDLE_DIR &&
    segments[5] === BUNDLE_FILE
  );
}

import type { EdgeEntryKind, EdgeInvoker } from "@framework/next-router";

export type { EdgeEntryKind, EdgeInvoker };

export interface EdgeRuntime {
  loader: WorkerLoader;
  envelopeKey?: string;
}

export interface EdgeRelease {
  edgeWorkers: EdgeWorkers;
  isrPrefix: string;
  env?: Record<string, string>;
  envelope?: string;
  releaseFingerprint?: string;
}

export function createReleaseEdgeInvoker(
  runtime: EdgeRuntime,
  store: EdgeObjectStore,
  record: EdgeRelease,
): EdgeInvoker {
  return createEdgeInvoker(runtime.loader, record.edgeWorkers, store, {
    scope: record.isrPrefix,
    env: record.env,
    envelope: record.envelope,
    envelopeKey: runtime.envelopeKey,
    releaseFingerprint: record.releaseFingerprint,
  });
}

export function createEdgeInvoker(
  loader: WorkerLoader,
  workers: EdgeWorkers,
  store: EdgeObjectStore,
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
    if (bundle.version !== edgeBundleVersion) {
      throw new Error(
        `ocel: edge bundle at ${workers.bundleKey} is version ${bundle.version}, which this worker cannot read`,
      );
    }

    const modules: Record<string, WorkerLoaderModule> = {
      [WORKERS_SHIM_MODULE]: { js: renderWorkersShim(bundle) },
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
      mainModule: WORKERS_SHIM_MODULE,
      modules,
      env: {
        ...(bundle.env ?? {}),
        ...(variables?.env ?? {}),
        ...(envelope && sealed
          ? await unseal(
              await dataKey(envelope, variables?.envelopeKey),
              await sealed.arrayBuffer(),
            )
          : {}),
      },
    };
  };

  const idFor = (kind: EdgeEntryKind) =>
    `edge:${JSON.stringify([
      workers.id,
      variables?.scope ?? null,
      variables?.releaseFingerprint ?? null,
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

async function aesKey(base64: string): Promise<CryptoKey> {
  return crypto.subtle.importKey("raw", base64Bytes(base64), { name: "AES-GCM" }, false, [
    "decrypt",
  ]);
}

async function openFramed(key: CryptoKey, framed: ArrayBuffer): Promise<ArrayBuffer> {
  const bytes = new Uint8Array(framed);
  return crypto.subtle.decrypt(
    { name: "AES-GCM", iv: bytes.subarray(0, NONCE_BYTES) },
    key,
    bytes.subarray(NONCE_BYTES),
  );
}

async function dataKey(envelope: string, envelopeKey: string | undefined): Promise<ArrayBuffer> {
  if (envelopeKey === undefined) return base64Bytes(envelope);
  try {
    return await openFramed(await aesKey(envelopeKey), base64Bytes(envelope));
  } catch {
    throw new Error("ocel: the envelope was not wrapped for this worker");
  }
}

async function unseal(dataKey: ArrayBuffer, sealed: ArrayBuffer): Promise<Record<string, string>> {
  const key = await crypto.subtle.importKey("raw", dataKey, { name: "AES-GCM" }, false, [
    "decrypt",
  ]);
  const payload = await openFramed(key, sealed);
  try {
    return JSON.parse(new TextDecoder().decode(payload)) as Record<string, string>;
  } catch {
    throw new Error("ocel: sealed edge variables are not JSON");
  }
}

function base64Bytes(base64: string): ArrayBuffer {
  const binary = atob(base64);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
  return bytes.buffer;
}
