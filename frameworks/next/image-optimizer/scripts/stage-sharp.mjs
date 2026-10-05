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
}
