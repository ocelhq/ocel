// Builds the deployable zip: one bundled ESM entrypoint plus sharp's Linux
// arm64 native binaries, ready for PR 5b's CLI to upload into a customer's
// bucket.
//
// The native half is cross-installed rather than compiled. pnpm's
// supportedArchitectures setting tells it which optional platform packages to
// resolve, so a glibc arm64 sharp is fetched from the registry on any host — no
// Docker, no qemu, and the same bytes in CI as on a laptop.
//
// The install runs in a scratch directory with its own pnpm-workspace.yaml,
// because pnpm 11 reads that setting from the workspace manifest rather than from
// a package.json `pnpm` field, and a pnpm-workspace.yaml anywhere under
// packages/ would make that directory a second workspace root for the whole
// monorepo. The scratch tree is also what keeps this repo's own node_modules from
// ever being asked to hold a foreign architecture.
//
// The zip's layout is what Lambda expects of a plain Node function:
//   index.mjs          the bundle
//   node_modules/      sharp and its @img/* platform packages
const TARGET = { os: ["linux"], cpu: ["arm64"], libc: ["glibc"] };

import { execFileSync } from "node:child_process";
import { mkdirSync, readFileSync, rmSync, statSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = dirname(dirname(fileURLToPath(import.meta.url)));
const manifest = JSON.parse(readFileSync(join(root, "package.json"), "utf8"));
const out = join(root, "dist", "zip");
const stage = join(root, "dist", "stage");

rmSync(out, { recursive: true, force: true });
rmSync(stage, { recursive: true, force: true });
mkdirSync(stage, { recursive: true });
mkdirSync(out, { recursive: true });

// sharp is external: a native addon cannot be bundled, and it is the only
// dependency that has to arrive as a real node_modules tree. Everything else —
// the AWS SDK, undici, ipaddr.js — is pure JS and folds into the one file.
execFileSync(
  "pnpm",
  [
    "exec",
    "esbuild",
    join(root, "src", "index.mts"),
    "--bundle",
    "--platform=node",
    "--target=node22",
    "--format=esm",
    "--external:sharp",
    `--outfile=${join(out, "index.mjs")}`,
  ],
  { cwd: root, stdio: "inherit" },
);

// sharp's version comes from this package's own dependencies, so the artifact and
// the tests can never be built against different libvips.
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

// packages: [] makes the scratch directory its own workspace root, which both
// carries the architecture setting and stops pnpm walking up into this monorepo.
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

// Hoisted linking because what Lambda unzips is a plain directory: nothing there
// resolves a symlink farm.
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

// pnpm's own bookkeeping records absolute paths and an install timestamp, so it
// would make the digest differ per machine and per run. Lambda reads none of it.
for (const file of [
  ".modules.yaml",
  ".package-map.json",
  ".pnpm-workspace-state-v1.json",
  ".pnpm",
]) {
  rmSync(join(out, "node_modules", file), { recursive: true, force: true });
}

// Fixed timestamps, no extra attributes, and a sorted entry list, so identical
// inputs produce an identical zip: PR 5b's CLI pins this artifact by sha256 and
// verifies it fail-closed, which only means anything if the digest is
// reproducible.
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
