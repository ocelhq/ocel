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
const binaryName = "deploy";

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
