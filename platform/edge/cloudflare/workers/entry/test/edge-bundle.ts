import { env } from "cloudflare:test";

declare module "cloudflare:test" {
  interface ProvidedEnv {
    LOADER: WorkerLoader;
  }
}

interface EdgeEntry {
  chunks: string[];
  handlerExport: string;
}

export function shimFor(
  entries: Record<string, EdgeEntry>,
  assetIdByName: Record<string, string> = {},
): string {
  return `import { AsyncLocalStorage } from "node:async_hooks"

const ENTRIES = ${JSON.stringify(entries)}
const ASSETS = ${JSON.stringify(assetIdByName)}

const ocelFetch = globalThis.fetch

const ocelAssetId = (url) => {
  if (typeof url !== "string" || !url.startsWith("blob:")) return undefined
  const name = url.slice(5)
  if (Object.hasOwn(ASSETS, name)) return ASSETS[name]
  let decoded
  try {
    decoded = decodeURIComponent(name)
  } catch {
    return undefined
  }
  return Object.hasOwn(ASSETS, decoded) ? ASSETS[decoded] : undefined
}

globalThis.fetch = (input, init) => {
  const url =
    typeof input === "string" ? input : input instanceof URL ? input.href : input?.url
  const id = ocelAssetId(url)
  if (id === undefined) return ocelFetch(input, init)
  return import("./" + id).then((m) => new Response(m.default))
}

export default {
  async fetch(request, env, ctx) {
    globalThis.AsyncLocalStorage ??= AsyncLocalStorage
    globalThis.process ??= { env: {} }
    Object.assign(globalThis.process.env, env)
    const k = ctx.props.entryKey
    globalThis.__OCEL_EDGE_ENTRY = k
    const e = ENTRIES[k]
    if (!e) return new Response(\`unknown edge entry \${k}\`, { status: 500 })
    for (const id of e.chunks) await import("./" + id)
    const entry = await globalThis._ENTRIES[k]
    const handler = entry[e.handlerExport]
    return handler(request, {
      waitUntil: (p) => ctx.waitUntil(p),
      signal: request.signal,
      requestMeta: {},
    })
  },
}
`;
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
  const entries: Record<string, EdgeEntry> = {};
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
    version: options.version ?? 2,
    mainModule: "main.js",
    shim: shimFor(entries, assetIdByName),
    chunks,
    wasm: {},
    assets,
    env: options.env ?? { __NEXT_BUILD_ID: "t" },
    entries,
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
