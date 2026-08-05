// Builds the deployable zip: one bundled ESM entrypoint, and nothing else.
//
//   index.mjs          the bundle
//
// Fixed timestamps and a sorted entry list, so identical inputs produce an
// identical zip: bootstrap pins this artifact by sha256 and verifies it
// fail-closed, which only means anything if the digest is reproducible.

import { execFileSync } from "node:child_process";
import { mkdirSync, rmSync, statSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import { esbuildArgs } from "./bundle.mjs";

const root = dirname(dirname(fileURLToPath(import.meta.url)));
const out = join(root, "dist", "zip");

rmSync(out, { recursive: true, force: true });
mkdirSync(out, { recursive: true });

execFileSync(
  "pnpm",
  ["exec", "esbuild", ...esbuildArgs(join(root, "src", "index.mts"), join(out, "index.mjs"))],
  { cwd: root, stdio: "inherit" },
);

execFileSync("find", [out, "-exec", "touch", "-t", "198001010000", "{}", "+"], {
  stdio: "inherit",
});
const entries = execFileSync("find", [".", "-mindepth", "1"], { cwd: out, encoding: "utf8" })
  .split("\n")
  .filter(Boolean)
  .sort()
  .join("\n");

const zip = join(root, "dist", "revalidator.zip");
rmSync(zip, { force: true });
execFileSync("zip", ["-X", "-q", "-@", zip], {
  cwd: out,
  input: entries,
  stdio: ["pipe", "inherit", "inherit"],
});

console.log(`zip ${statSync(zip).size} bytes at ${zip}`);
