import { chmodSync, copyFileSync, mkdirSync, readFileSync } from "node:fs";
import { basename, join } from "node:path";

const [dist, packages] = process.argv.slice(2);
if (!dist || !packages) {
  console.error("usage: platform-packages.mjs <dist directory> <packages directory>");
  process.exit(1);
}

const operatingSystems = { darwin: "darwin", linux: "linux", windows: "win32" };
const architectures = { amd64: "x64", arm64: "arm64" };

const wanted = new Set(
  Object.values(operatingSystems).flatMap((os) =>
    Object.values(architectures)
      .filter((arch) => !(os === "win32" && arch === "arm64"))
      .map((arch) => `cli-${os}-${arch}`),
  ),
);

const artifacts = JSON.parse(readFileSync(join(dist, "artifacts.json"), "utf8"));

for (const artifact of artifacts) {
  if (artifact.type !== "Binary" || artifact.extra?.ID !== "ocel") {
    continue;
  }
  const target = `cli-${operatingSystems[artifact.goos]}-${architectures[artifact.goarch]}`;
  if (!wanted.delete(target)) {
    continue;
  }
  const bin = join(packages, target, "bin");
  mkdirSync(bin, { recursive: true });
  const placed = join(bin, basename(artifact.path));
  copyFileSync(artifact.path, placed);
  chmodSync(placed, 0o755);
  console.log(`${artifact.path} -> ${placed}`);
}

if (wanted.size > 0) {
  console.error(`goreleaser built no ocel binary for ${[...wanted].sort().join(", ")}`);
  process.exit(1);
}
