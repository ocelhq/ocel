#!/usr/bin/env node

import { readFileSync } from "node:fs";
import { join } from "node:path";
import { parse } from "./version.mjs";

const DOWNLOADS = "https://github.com/ocelhq/ocel/releases/download";

export function cask(version, checksums) {
  if (parse(version).channel !== "stable") {
    throw new Error(`the tap carries stable releases only, not ${version}`);
  }
  const sums = new Map(
    checksums
      .split("\n")
      .map((line) => line.trim().split(/\s+/))
      .filter((fields) => fields.length === 2)
      .map(([sum, name]) => [name, sum]),
  );
  const variant = (os, arch) => {
    const name = `ocel_${version}_${os}_${arch}.tar.gz`;
    const sum = sums.get(name);
    if (!sum) throw new Error(`checksums.txt names no ${name}`);
    return [`      sha256 "${sum}"`, `      url "${DOWNLOADS}/v${version}/${name}"`].join("\n");
  };
  return `cask "ocel" do
  version "${version}"

  on_macos do
    on_arm do
${variant("darwin", "arm64")}
    end
    on_intel do
${variant("darwin", "amd64")}
    end
  end
  on_linux do
    on_arm do
${variant("linux", "arm64")}
    end
    on_intel do
${variant("linux", "amd64")}
    end
  end

  name "ocel"
  desc "Deploys apps into your own cloud"
  homepage "https://ocel.dev"

  livecheck do
    url :url
    strategy :github_latest
  end

  binary "ocel"

  postflight do
    if OS.mac?
      system_command "/usr/bin/xattr", args: ["-dr", "com.apple.quarantine", "#{staged_path}/ocel"]
    end
  end
end
`;
}

function main() {
  const [archives, version] = process.argv.slice(2);
  if (!archives || !version) {
    console.error("usage: cask.mjs <release archives directory> <version>");
    process.exit(1);
  }
  process.stdout.write(cask(version, readFileSync(join(archives, "checksums.txt"), "utf8")));
}

if (import.meta.main) main();
