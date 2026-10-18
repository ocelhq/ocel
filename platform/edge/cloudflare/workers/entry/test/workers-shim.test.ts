import { env } from "cloudflare:test";
import { type EdgeBundle, edgeBundleVersion } from "@platform/edge-contract/edge-bundle";
import { describe, expect, it } from "vitest";
import { renderWorkersShim, WORKERS_SHIM_MODULE } from "../src/workers-shim";

declare module "cloudflare:test" {
  interface ProvidedEnv {
    LOADER: WorkerLoader;
  }
}

function bundle(over: Partial<EdgeBundle> = {}): EdgeBundle {
  return {
    version: edgeBundleVersion,
    entries: { middleware_middleware: { chunks: ["c/0.js"], handlerExport: "handler" } },
    chunks: { "c/0.js": "" },
    wasmGlobals: { wasm_hello: "w/0.wasm" },
    // biome-ignore lint/suspicious/noTemplateCurlyInString: the asset name under test contains a literal ${c}
    assetNames: { "server/edge/assets/a`b${c}.abc.txt": "a/0.bin" },
    clientAssetSuffix: "?dpl=3f7c1b9a5e2d4c8f",
    ...over,
  };
}

let loaded = 0;

describe("the Workers shim an edge bundle runs under", () => {
  it("gives the edge runtime the client asset suffix the bundle names", () => {
    expect(renderWorkersShim(bundle())).toContain(
      'globalThis.NEXT_CLIENT_ASSET_SUFFIX = "?dpl=3f7c1b9a5e2d4c8f"',
    );
  });

  it("inlines the entries table and imports only a hit entry's chunks, after the env is set", () => {
    const shim = renderWorkersShim(bundle());

    expect(shim).toContain("middleware_middleware");
    expect(shim).toContain("ctx.props.entryKey");
    expect(shim.indexOf("Object.assign(globalThis.process.env, env)")).toBeLessThan(
      shim.indexOf("await import"),
    );
  });

  it("hands the running entry key to the chunks on a global, before they evaluate", () => {
    const shim = renderWorkersShim(bundle());

    expect(shim.indexOf("globalThis.__OCEL_EDGE_ENTRY = k")).toBeLessThan(
      shim.indexOf("for (const id of e.chunks)"),
    );
  });

  it("binds each wasm module to the global its chunks reach for, before they evaluate", () => {
    const shim = renderWorkersShim(bundle());

    expect(shim).toContain('const WASM = {"wasm_hello":"w/0.wasm"}');
    expect(shim.indexOf("globalThis[name] ??=")).toBeLessThan(
      shim.indexOf("for (const id of e.chunks)"),
    );
  });

  it("installs no fetch cache of its own", () => {
    const shim = renderWorkersShim(bundle());

    expect(shim).not.toContain("@next/cache-handlers");
    expect(shim).not.toContain("__OCEL_EDGE_CACHE");
  });

  it("loads as a Workers module for an asset name containing template syntax", async () => {
    const worker = env.LOADER.get(`shim-${loaded++}`, async () => ({
      compatibilityDate: "2026-03-10",
      compatibilityFlags: ["nodejs_compat"],
      mainModule: WORKERS_SHIM_MODULE,
      modules: { [WORKERS_SHIM_MODULE]: { js: renderWorkersShim(bundle()) } },
    }));

    const response = await worker
      .getEntrypoint<undefined>(undefined, { props: { entryKey: "absent" } })
      .fetch(new Request("https://app.example/"));

    expect(response.status).toBe(500);
    expect(await response.text()).toBe("unknown edge entry absent");
  });
});
