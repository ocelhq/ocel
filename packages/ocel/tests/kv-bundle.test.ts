import { execFileSync } from "node:child_process";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { build } from "esbuild";
import { afterEach, beforeEach, expect, it } from "vitest";

const loader = fileURLToPath(new URL("../src/kv/ioredis.ts", import.meta.url));

const openClient = [
  `import { newRedis } from ${JSON.stringify(loader)};`,
  "try {",
  "  const client = newRedis({ lazyConnect: true });",
  "  console.log(client.status);",
  "  client.disconnect();",
  "} catch (error) {",
  "  console.log(error.message);",
  "}",
].join("\n");

const requireForBundledCommonJS =
  'import { createRequire as bannerCreateRequire } from "node:module";' +
  "const require = bannerCreateRequire(import.meta.url);";

let bundleDir: string;

beforeEach(() => {
  bundleDir = mkdtempSync(join(tmpdir(), "ocel-kv-bundle-"));
});

afterEach(() => {
  rmSync(bundleDir, { recursive: true, force: true });
});

async function runBundle(external: string[] = []): Promise<string> {
  const outfile = join(bundleDir, "index.mjs");
  await build({
    stdin: { contents: openClient, resolveDir: bundleDir, loader: "ts" },
    bundle: true,
    platform: "node",
    format: "esm",
    banner: { js: requireForBundledCommonJS },
    external,
    outfile,
    logLevel: "silent",
  });
  const { NODE_PATH: _, ...environment } = process.env;
  return execFileSync(process.execPath, [outfile], {
    cwd: bundleDir,
    env: environment,
    encoding: "utf8",
    stdio: ["ignore", "pipe", "pipe"],
  }).trim();
}

it("opens a kv store's ioredis client from a bundle with no node_modules beside it", async () => {
  expect(await runBundle()).toBe("wait");
});

it("says to install ioredis when neither the bundle nor node_modules holds it", async () => {
  expect(await runBundle(["ioredis"])).toBe(
    "a kv store is reached with ioredis, which is not installed. Install it: npm install ioredis",
  );
});
