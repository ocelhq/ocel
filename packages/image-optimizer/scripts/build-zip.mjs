const TARGET = { os: ["linux"], cpu: ["arm64"], libc: ["glibc"] };

import { execFileSync } from "node:child_process";
import { mkdirSync, readFileSync, rmSync, statSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import { esbuildArgs } from "./bundle.mjs";

const root = dirname(dirname(fileURLToPath(import.meta.url)));
const manifest = JSON.parse(readFileSync(join(root, "package.json"), "utf8"));
const out = join(root, "dist", "zip");
const stage = join(root, "dist", "stage");

rmSync(out, { recursive: true, force: true });
rmSync(stage, { recursive: true, force: true });
mkdirSync(stage, { recursive: true });
mkdirSync(out, { recursive: true });

execFileSync(
  "pnpm",
  ["exec", "esbuild", ...esbuildArgs(join(root, "src", "index.mts"), join(out, "index.mjs"))],
  { cwd: root, stdio: "inherit" },
);

writeFileSync(
  join(stage, "package.json"),
  `${JSON.stringify(
    {
      name: "ocel-image-optimizer-runtime",
      private: true,
      dependencies: { sharp: manifest.dependencies.sharp },
    },
    null,
    2,
  )}\n`,
);

writeFileSync(
  join(stage, "pnpm-workspace.yaml"),
  [
    "packages: []",
    "supportedArchitectures:",
    `  os: [${TARGET.os.join(", ")}]`,
    `  cpu: [${TARGET.cpu.join(", ")}]`,
    `  libc: [${TARGET.libc.join(", ")}]`,
    "",
  ].join("\n"),
);

execFileSync(
  "pnpm",
  ["install", "--node-linker=hoisted", "--prod", "--no-frozen-lockfile"],
  { cwd: stage, stdio: "inherit" },
);

execFileSync("cp", ["-R", join(stage, "node_modules"), join(out, "node_modules")], {
  stdio: "inherit",
});

if (!statSync(join(out, "node_modules", "@img", "sharp-linux-arm64"), { throwIfNoEntry: false })) {
  throw new Error("cross-install produced no @img/sharp-linux-arm64");
}
if (
  !statSync(join(out, "node_modules", "@img", "sharp-libvips-linux-arm64"), {
    throwIfNoEntry: false,
  })
) {
  throw new Error("cross-install produced no @img/sharp-libvips-linux-arm64");
}

for (const file of [
  ".modules.yaml",
  ".package-map.json",
  ".pnpm-workspace-state-v1.json",
  ".pnpm",
]) {
  rmSync(join(out, "node_modules", file), { recursive: true, force: true });
}

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
execFileSync("zip", ["-X", "-q", "-@", zip], { cwd: out, input: entries, stdio: ["pipe", "inherit", "inherit"] });

const unzipped = Number(
  execFileSync("du", ["-sb", out], { encoding: "utf8" }).split(/\s/)[0],
);
const CAP = 250 * 1024 * 1024;
console.log(`unzipped ${unzipped} bytes (${(unzipped / 1e6).toFixed(1)} MB), cap ${CAP}`);
console.log(`zip ${statSync(zip).size} bytes at ${zip}`);
if (unzipped > CAP) {
  throw new Error(`unzipped size ${unzipped} exceeds the ${CAP} byte cap`);
}
