import { execFileSync } from "node:child_process";
import {
  cpSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  realpathSync,
  rmSync,
  statSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const pkgDir = dirname(dirname(fileURLToPath(import.meta.url)));

const pnpmMetadata = [
  ".modules.yaml",
  ".package-map.json",
  ".pnpm-workspace-state-v1.json",
  ".pnpm",
];

const manifestOf = (dir) => JSON.parse(readFileSync(join(dir, "package.json"), "utf8"));

const libvipsReleases = "https://github.com/lovell/sharp-libvips/releases/tag";

const licenseTexts = ["LGPL-3.0.txt", "GPL-3.0.txt"];

export function stageSharp(out, cpu) {
  const sharpDir = realpathSync(join(pkgDir, "node_modules", "sharp"));
  const sharpManifest = manifestOf(sharpDir);
  const locked = Object.keys(sharpManifest.dependencies).map(
    (name) => `  "${name}": ${manifestOf(join(sharpDir, "..", name)).version}`,
  );
  const stage = mkdtempSync(join(tmpdir(), "ocel-sharp-"));
  try {
    writeFileSync(
      join(stage, "package.json"),
      `${JSON.stringify(
        {
          name: "ocel-sharp-runtime",
          private: true,
          dependencies: { sharp: sharpManifest.version },
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
        "  os: [linux]",
        `  cpu: [${cpu}]`,
        "  libc: [glibc]",
        "overrides:",
        ...locked,
        "",
      ].join("\n"),
    );
    execFileSync("pnpm", ["install", "--node-linker=hoisted", "--prod", "--no-frozen-lockfile"], {
      cwd: stage,
      stdio: "inherit",
    });
    mkdirSync(out, { recursive: true });
    cpSync(join(stage, "node_modules"), join(out, "node_modules"), {
      recursive: true,
      verbatimSymlinks: true,
    });
  } finally {
    rmSync(stage, { recursive: true, force: true });
  }
  for (const file of pnpmMetadata) {
    rmSync(join(out, "node_modules", file), { recursive: true, force: true });
  }
  for (const built of [`sharp-linux-${cpu}`, `sharp-libvips-linux-${cpu}`]) {
    if (!statSync(join(out, "node_modules", "@img", built), { throwIfNoEntry: false })) {
      throw new Error(`cross-install produced no @img/${built}`);
    }
  }
  writeLibvipsNotice(out, cpu);
}

export function writeLibvipsNotice(out, cpu) {
  const packagePath = `node_modules/@img/sharp-libvips-linux-${cpu}`;
  const libvips = join(out, packagePath);
  const { version } = manifestOf(libvips);
  const { vips } = JSON.parse(readFileSync(join(libvips, "versions.json"), "utf8"));
  writeFileSync(
    join(out, "THIRD_PARTY_NOTICES"),
    [
      `This directory contains libvips ${vips} and the libraries it bundles, as built by sharp-libvips v${version}, in ${packagePath}.`,
      "They are licensed under the GNU Lesser General Public License, version 3 or any later version (LGPL-3.0-or-later).",
      "The LGPL-3.0 and the GNU General Public License, version 3, which it incorporates, are in LGPL-3.0.txt and GPL-3.0.txt beside this file.",
      `The corresponding source is published at ${libvipsReleases}/v${version}.`,
      "",
    ].join("\n"),
  );
  for (const text of licenseTexts) {
    cpSync(join(pkgDir, "licenses", text), join(out, text));
  }
}
