// Resolves a provider's platform-specific binary via Node's own
// require.resolve, mirroring packages/ocel/bin/run.js's own platform-binary
// resolution. Invoked by the CLI as:
//
//   node resolve-provider.cjs <package-name>
//
// e.g. <package-name> = "@ocel/provider-aws". Run from inside the user's
// project (see providerlocator.Locate), so require.resolve walks that
// project's own node_modules — whatever npm/pnpm/yarn actually installed —
// rather than wherever this script happens to be written to.
//
// Prints the resolved absolute binary path to stdout on success. On
// failure, prints a clear diagnostic to stderr and exits non-zero.

const { join } = require("path");

const packageName = process.argv[2];
if (!packageName) {
  console.error("usage: node resolve-provider.cjs <package-name>");
  process.exit(1);
}

const providerPrefix = "@ocel/provider-";
if (!packageName.startsWith(providerPrefix)) {
  console.error(
    `Provider package ${packageName} does not follow the @ocel/provider-<name> convention.`,
  );
  process.exit(1);
}
// The binary inside each platform package is named after the provider
// itself (e.g. "aws"), matching how `go install .../cloud/aws` names it —
// not the descriptor's own package name.
const binaryName = packageName.slice(providerPrefix.length);

const { platform, arch } = process;

let platformSuffix;
switch (platform) {
  case "win32":
    platformSuffix = `win32-${arch}`;
    break;
  case "darwin":
    platformSuffix = `darwin-${arch}`;
    break;
  case "linux":
    platformSuffix = `linux-${arch}`;
    break;
  default:
    console.error(`Unsupported platform: ${platform}`);
    process.exit(1);
}

const platformPackage = `${packageName}-${platformSuffix}`;
const binary = platform === "win32" ? `${binaryName}.exe` : binaryName;

try {
  const binaryPath = require.resolve(join(platformPackage, "bin", binary));
  process.stdout.write(binaryPath);
} catch (e) {
  console.error(
    `Failed to locate binary for ${platformPackage}. Is ${packageName} installed? Run \`npm install ${packageName}\` (or add it as a dependency via your package manager).`,
  );
  process.exit(1);
}
