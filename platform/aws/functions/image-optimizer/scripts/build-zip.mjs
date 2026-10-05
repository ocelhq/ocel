import { execFileSync } from "node:child_process";
import { mkdirSync, readdirSync, rmSync, statSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import { stageSharp } from "@framework/next-image-optimizer/stage-sharp";

import { bunArgs } from "./bundle.mjs";

const root = dirname(dirname(fileURLToPath(import.meta.url)));
const out = join(root, "dist", "zip");

rmSync(out, { recursive: true, force: true });
mkdirSync(out, { recursive: true });

execFileSync("bun", ["build", ...bunArgs(join(root, "src", "index.mts"), join(out, "index.mjs"))], {
  cwd: root,
  stdio: "inherit",
});

stageSharp(out, "arm64");

execFileSync("chmod", ["-R", "u=rwX,go=rX", out], { stdio: "inherit" });
execFileSync("find", [out, "-exec", "touch", "-t", "198001010000", "{}", "+"], {
  stdio: "inherit",
});
const entries = execFileSync("find", [".", "-mindepth", "1"], { cwd: out, encoding: "utf8" })
  .split("\n")
  .filter(Boolean)
  .sort()
  .join("\n");
const zip = join(root, "dist", "image-optimizer.zip");
rmSync(zip, { force: true });
execFileSync("zip", ["-X", "-q", "-@", zip], {
  cwd: out,
  input: entries,
  stdio: ["pipe", "inherit", "inherit"],
});

function unzippedSize(dir) {
  let total = 0;
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name);
    if (entry.isDirectory()) {
      total += unzippedSize(full);
    } else if (entry.isFile()) {
      total += statSync(full).size;
    }
  }
  return total;
}

const unzipped = unzippedSize(out);
const CAP = 250 * 1024 * 1024;
console.log(`unzipped ${unzipped} bytes (${(unzipped / 1e6).toFixed(1)} MB), cap ${CAP}`);
console.log(`zip ${statSync(zip).size} bytes at ${zip}`);
if (unzipped > CAP) {
  throw new Error(`unzipped size ${unzipped} exceeds the ${CAP} byte cap`);
}
