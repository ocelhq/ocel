import { existsSync, lstatSync, mkdirSync, rmSync, symlinkSync } from "node:fs";
import { join } from "node:path";

const NATIVE_SUFFIX = { linux: "linux-x64", darwin: `darwin-${process.arch}`, win32: "win32-x64" };

export function nativeSuffix(platform = process.platform) {
  const suffix = NATIVE_SUFFIX[platform];
  if (!suffix) {
    throw new Error(`no ocel native package for ${platform}/${process.arch}`);
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

export function assertToolchain(repoRoot) {
  const missing = missingToolchainArtifacts(repoRoot);
  if (missing.length === 0) {
    return;
  }
  throw new Error(
    "the toolchain this suite consumes with is incomplete:\n" +
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
