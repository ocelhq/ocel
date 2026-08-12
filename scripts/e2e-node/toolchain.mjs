import { spawnSync } from "node:child_process";
import { existsSync, lstatSync, mkdirSync, rmSync, symlinkSync } from "node:fs";
import { join } from "node:path";

export const SKIP_BUILD_ENV = "OCEL_E2E_NODE_SKIP_BUILD";

const NATIVE_SUFFIX = { linux: "linux-x64", darwin: `darwin-${process.arch}`, win32: "win32-x64" };

function nativeSuffix() {
  const suffix = NATIVE_SUFFIX[process.platform];
  if (!suffix) {
    throw new Error(`no ocel native package for ${process.platform}/${process.arch}`);
  }
  return suffix;
}

export function toolchainArtifacts(repoRoot) {
  const suffix = nativeSuffix();
  const exe = process.platform === "win32" ? ".exe" : "";
  return [
    {
      what: "the ocel package's compiled dist",
      path: join(repoRoot, "packages", "ocel", "dist", "config.js"),
      how: "pnpm --filter ocel build",
    },
    {
      what: "the ocel CLI binary, which embeds the node builder",
      path: join(repoRoot, "packages", "native-lib", `cli-${suffix}`, "bin", `ocel${exe}`),
      how: "node scripts/build-native.mjs --host --target cli",
    },
    {
      what: "the AWS provider's deploy binary",
      path: join(repoRoot, "packages", "native-lib", `provider-aws-${suffix}`, "bin", `deploy${exe}`),
      how: "node scripts/build-native.mjs --host --target provider",
    },
    {
      what: "the AWS provider's runtime binary",
      path: join(repoRoot, "packages", "native-lib", `provider-aws-${suffix}`, "bin", `runtime${exe}`),
      how: "node scripts/build-native.mjs --host --target provider",
    },
  ];
}

export function missingToolchainArtifacts(repoRoot) {
  return toolchainArtifacts(repoRoot).filter((artifact) => !existsSync(artifact.path));
}

export function buildToolchain(repoRoot) {
  if (process.env[SKIP_BUILD_ENV] === "1") {
    console.error(
      `[ocel-e2e-node] ${SKIP_BUILD_ENV}=1: reusing whatever is already built. A binary older than ` +
        `the sources it was built from deploys the old behaviour and reports success.`,
    );
    assertToolchain(repoRoot);
    return;
  }

  runIn(repoRoot, "pnpm --filter ocel build", "pnpm", ["--filter", "ocel", "build"]);
  runIn(repoRoot, "build the ocel CLI", process.execPath, [
    join("scripts", "build-native.mjs"),
    "--host",
    "--target",
    "cli",
  ]);
  runIn(repoRoot, "build the AWS provider", process.execPath, [
    join("scripts", "build-native.mjs"),
    "--host",
    "--target",
    "provider",
  ]);
  assertToolchain(repoRoot);
}

export function assertToolchain(repoRoot) {
  const missing = missingToolchainArtifacts(repoRoot);
  if (missing.length === 0) {
    return;
  }
  throw new Error(
    `the toolchain this suite deploys with is incomplete:\n` +
      missing.map((a) => `  - ${a.what} is not at ${a.path}; build it with \`${a.how}\``).join("\n"),
  );
}

export function linkOcel(dir, repoRoot) {
  const modules = join(dir, "node_modules");
  const links = [
    { link: join(modules, "ocel"), target: join(repoRoot, "packages", "ocel") },
    { link: join(modules, "@ocel", "provider-aws"), target: join(repoRoot, "packages", "provider-aws") },
  ];
  for (const { link, target } of links) {
    if (!existsSync(join(target, "package.json"))) {
      throw new Error(`${target} is not a package; is ${repoRoot} the ocel repo root?`);
    }
    mkdirSync(join(link, ".."), { recursive: true });
    if (existsSync(link) || isSymlink(link)) {
      rmSync(link, { recursive: true, force: true });
    }
    symlinkSync(target, link, "dir");
  }
}

function isSymlink(path) {
  try {
    return lstatSync(path).isSymbolicLink();
  } catch {
    return false;
  }
}

function runIn(cwd, label, command, args) {
  console.error(`[ocel-e2e-node] ${label}`);
  const res = spawnSync(command, args, { cwd, stdio: ["ignore", "inherit", "inherit"] });
  if (res.error) {
    throw new Error(`${label}: ${res.error.message}`);
  }
  if (res.status !== 0) {
    throw new Error(`${label} exited with ${res.status}`);
  }
}
