import { execFileSync } from "node:child_process";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import { tagNamespace } from "@framework/next-cache";
import { afterAll, expect, it } from "vitest";

import { bunArgs } from "../scripts/bundle.mjs";

const namespace = tagNamespace("prod/acme/web/r0a1b2c3d/isr")!;

const raised = {
  dynamodb: {
    SequenceNumber: "seq-1",
    NewImage: {
      gsi1pk: { S: namespace },
      tag: { S: "products" },
      stale: { N: "100" },
    },
  },
};

const root = dirname(dirname(fileURLToPath(import.meta.url)));
const out = mkdtempSync(join(tmpdir(), "ocel-tag-invalidator-"));

afterAll(() => rmSync(out, { recursive: true, force: true }));

it("builds a bundle that loads and exports the handler", async () => {
  const outfile = join(out, "index.mjs");
  execFileSync("bun", ["build", ...bunArgs(join(root, "src", "index.mts"), outfile)], {
    cwd: root,
    stdio: "inherit",
  });

  const bundle = await import(outfile);
  expect(typeof bundle.handler).toBe("function");
  await expect(bundle.handler({ Records: [] })).resolves.toEqual({ batchItemFailures: [] });
  await expect(bundle.handler({ Records: [raised] })).rejects.toThrow("OCEL_STATE_TABLE");
}, 60_000);
