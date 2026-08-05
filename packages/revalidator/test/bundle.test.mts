import { execFileSync } from "node:child_process";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import { afterAll, expect, it } from "vitest";

import { esbuildArgs } from "../scripts/bundle.mjs";

// The bundle is what Lambda actually runs, and the ways it fails are not type
// errors: a dependency that reaches for `require` at module scope throws while
// the module graph is still loading, on every invocation, before the handler
// exists. Importing the built bundle is the only thing that proves the flags
// the zip is built with produce a loadable function.
const root = dirname(dirname(fileURLToPath(import.meta.url)));
const out = mkdtempSync(join(tmpdir(), "ocel-revalidator-"));

afterAll(() => rmSync(out, { recursive: true, force: true }));

it("builds a bundle that loads and exports the handler", async () => {
  const outfile = join(out, "index.mjs");
  execFileSync("pnpm", ["exec", "esbuild", ...esbuildArgs(join(root, "src", "index.mts"), outfile)], {
    cwd: root,
    stdio: "inherit",
  });

  const bundle = await import(outfile);
  expect(typeof bundle.handler).toBe("function");
  // An empty batch reads no environment and sends nothing. The shape it answers
  // with is what the event source mapping's ReportBatchItemFailures reads, so an
  // empty batch has to carry it too.
  await expect(bundle.handler({ Records: [] })).resolves.toEqual({ batchItemFailures: [] });
}, 60_000);
