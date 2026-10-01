import { fileURLToPath } from "node:url";
import { build, type Rollup } from "vite";
import { describe, expect, it } from "vitest";

const entry = fileURLToPath(new URL("../testing/realtime-browser.ts", import.meta.url));

async function bundleForBrowser(): Promise<Rollup.OutputChunk[]> {
  const output = await build({
    configFile: false,
    logLevel: "silent",
    build: { write: false, minify: false, rollupOptions: { input: entry } },
  });
  return (Array.isArray(output) ? output : [output]).flatMap((bundle) =>
    "output" in bundle
      ? bundle.output.filter((file): file is Rollup.OutputChunk => file.type === "chunk")
      : [],
  );
}

describe("a browser bundle of the realtime client", () => {
  it("carries none of the server's rules, authorize, signing or node modules", async () => {
    const code = (await bundleForBrowser()).map((chunk) => chunk.code).join("\n");

    expect(code).toContain("connection_init");
    for (const server of [
      "authorize-marker-7d1c",
      "subscribe-rule-marker-41af",
      "publish-rule-marker-9b20",
      "signingKey",
      "createPrivateKey",
      "node:",
    ]) {
      expect(code, `the bundle carries ${server}`).not.toContain(server);
    }
  }, 30_000);

  it("loads the gateway's socket in its own chunk, fetched only when the handler names it", async () => {
    const chunks = await bundleForBrowser();

    expect(chunks.find((chunk) => chunk.isEntry)?.code).not.toContain("connection_init");
    expect(chunks.filter((chunk) => chunk.isDynamicEntry)).toHaveLength(1);
  }, 30_000);
});
