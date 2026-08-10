import { execFileSync } from "node:child_process";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import { afterAll, expect, it } from "vitest";

import { esbuildArgs } from "../scripts/bundle.mjs";

const root = dirname(dirname(fileURLToPath(import.meta.url)));
const out = mkdtempSync(join(tmpdir(), "ocel-tag-publisher-"));

afterAll(() => rmSync(out, { recursive: true, force: true }));

it("builds a bundle that loads and exports the handler", async () => {
  const outfile = join(out, "index.mjs");
  execFileSync("pnpm", ["exec", "esbuild", ...esbuildArgs(join(root, "src", "index.mts"), outfile)], {
    cwd: root,
    stdio: "inherit",
  });

  const bundle = await import(outfile);
  expect(typeof bundle.handler).toBe("function");
  await expect(bundle.handler({ Records: [] })).resolves.toEqual({ batchItemFailures: [] });
}, 60_000);
