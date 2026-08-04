import { execFileSync } from "node:child_process";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import { afterAll, expect, it } from "vitest";

import { esbuildArgs } from "../scripts/bundle.mjs";

// The bundle is what Lambda actually runs, and the way it can fail is not a
// type error: esbuild rewrites the AWS SDK's require() calls to a shim that
// throws unless a real `require` is in scope, which in a .mjs it is not. That
// throws while the module graph loads, on every invocation, before the handler
// exists — so importing the built bundle is the only thing that proves the
// banner is doing its job.
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
  // No records, no config read: it must not reach for SSM to do nothing. The
  // shape it answers with is what the event source mapping's
  // ReportBatchItemFailures reads, so an empty batch has to carry it too.
  await expect(bundle.handler({ Records: [] })).resolves.toEqual({ batchItemFailures: [] });
}, 60_000);
