const supported = new Set(["darwin-arm64", "darwin-x64", "linux-arm64", "linux-x64", "win32-x64"]);

/**
 * The name of the package carrying the `ocel` binary for a platform.
 *
 * @param {string} platform a `process.platform` value
 * @param {string} arch a `process.arch` value
 * @returns {string} the `@ocel/cli-<os>-<arch>` package name
 * @throws when ocel ships no binary for that pair
 */
export function platformPackage(platform, arch) {
  const target = `${platform}-${arch}`;
  if (!supported.has(target)) {
    throw new Error(
      `ocel ships no binary for ${target}; supported targets are ${[...supported].sort().join(", ")}`,
    );
  }
  return `@ocel/cli-${target}`;
}

/**
 * The path of the `ocel` binary installed for a platform.
 *
 * @param {object} target
 * @param {string} target.platform a `process.platform` value
 * @param {string} target.arch a `process.arch` value
 * @param {(specifier: string) => string} target.resolve resolves a module specifier to a file path
 * @returns {string} the path of the binary
 * @throws when ocel ships no binary for that pair, or the platform package is not installed
 */
export function binaryPath({ platform, arch, resolve }) {
  const name = platformPackage(platform, arch);
  const specifier = `${name}/bin/${platform === "win32" ? "ocel.exe" : "ocel"}`;
  try {
    return resolve(specifier);
  } catch (cause) {
    throw new Error(`${name} is not installed; reinstall @ocel/cli, or install ${name} directly`, {
      cause,
    });
  }
}
