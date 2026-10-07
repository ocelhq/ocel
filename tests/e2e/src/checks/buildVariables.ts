import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { readdir, readFile } from "node:fs/promises";
import path from "node:path";
import { type Check, json } from "./context";

export const BUILD_SENSITIVE_VALUE = "journey-build-sensitive-never-in-the-output";
export const BUILD_SECRET_VALUE = "journey-build-secret-never-in-the-output";

export const BUILD_VARIABLES: Record<string, string> = {
  BUILD_SENSITIVE: BUILD_SENSITIVE_VALUE,
  BUILD_SECRET: BUILD_SECRET_VALUE,
};

const LIVE_DIR_PREFIX = "ocel-live-";
const OUTPUT_DIR = path.join(".ocel", "output");

function sha256(value: string): string {
  return createHash("sha256").update(value).digest("hex");
}

async function filesUnder(dir: string): Promise<string[]> {
  const entries = await readdir(dir, { withFileTypes: true, recursive: true });
  return entries
    .filter((entry) => entry.isFile())
    .map((entry) => path.join(entry.parentPath, entry.name));
}

async function filesHoldingAValue(files: string[]): Promise<string[]> {
  const values = Object.values(BUILD_VARIABLES).map((value) => Buffer.from(value, "utf8"));
  const holding: string[] = [];
  for (const file of files) {
    const body = await readFile(file);
    if (values.some((value) => body.includes(value))) {
      holding.push(file);
    }
  }
  return holding;
}

export const buildVariablesChecks: Check[] = [
  {
    title:
      "GET /values/<slug> answers the digest of the sensitive and secret values it read at import",
    run: async (ctx) => {
      const { res, body } = await json(ctx, "/values/journey");
      assert.equal(res.status, 200);
      assert.deepEqual(body, {
        slug: "journey",
        sensitive: sha256(BUILD_SENSITIVE_VALUE),
        secret: sha256(BUILD_SECRET_VALUE),
      });
    },
  },
  {
    title: "no file under .ocel/output holds the sensitive or secret value the build read",
    run: async (ctx) => {
      const output = path.join(ctx.projectDir, OUTPUT_DIR);
      const files = await filesUnder(output).catch((error: unknown) =>
        assert.fail(`the deploy left no ${OUTPUT_DIR} to look through: ${String(error)}`),
      );
      assert.ok(files.length > 0, `the deploy left ${OUTPUT_DIR} empty`);
      const holding = await filesHoldingAValue(files);
      assert.deepEqual(
        holding.map((file) => path.relative(output, file)),
        [],
        `these files under ${OUTPUT_DIR} hold a sensitive or secret value`,
      );
    },
  },
  {
    title: "no ocel-live-* dir in the temp dir still holds a value the build read",
    run: async (ctx) => {
      const liveDirs = (await readdir(ctx.tempDir, { withFileTypes: true }))
        .filter((entry) => entry.isDirectory() && entry.name.startsWith(LIVE_DIR_PREFIX))
        .map((entry) => path.join(ctx.tempDir, entry.name));
      const left: string[] = [];
      for (const dir of liveDirs) {
        const files = await filesUnder(dir).catch(() => []);
        if ((await filesHoldingAValue(files)).length > 0) {
          left.push(path.basename(dir));
        }
      }
      assert.deepEqual(left, [], "the build left these live dirs behind, holding its values");
    },
  },
];

export function setsBuildVariables(checks: Check[]): boolean {
  return checks.some((one) => buildVariablesChecks.includes(one));
}
