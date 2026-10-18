import type { EdgeBundle } from "@platform/edge-contract/edge-bundle";

export const WORKERS_SHIM_MODULE = "main.js";

export function renderWorkersShim(bundle: EdgeBundle): string {
  return `import { AsyncLocalStorage } from "node:async_hooks"

globalThis.NEXT_CLIENT_ASSET_SUFFIX = ${JSON.stringify(bundle.clientAssetSuffix)}

const ENTRIES = ${JSON.stringify(bundle.entries)}
const ASSETS = ${JSON.stringify(bundle.assetNames ?? {})}
const WASM = ${JSON.stringify(bundle.wasmGlobals ?? {})}

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
    for (const [name, id] of Object.entries(WASM)) {
      globalThis[name] ??= (await import("./" + id)).default
    }
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
