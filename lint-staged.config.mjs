import { existsSync } from "node:fs";
import { dirname, join, relative } from "node:path";

const root = import.meta.dirname;

export default {
  "*.{js,mjs,cjs,ts,mts,cts,jsx,tsx,json,jsonc,css}":
    "biome check --write --no-errors-on-unmatched --files-ignore-unknown=true",
  "*.go": (files) =>
    modulesOf(files).map(
      ([module, packages]) =>
        `sh -c 'cd ${module} && golangci-lint fmt ${packages} && golangci-lint run ${packages}'`,
    ),
};

function modulesOf(files) {
  const grouped = new Map();
  for (const file of files) {
    const module = moduleOf(dirname(file));
    const pkg = relative(module, dirname(file));
    grouped.set(module, (grouped.get(module) ?? new Set()).add(pkg === "" ? "." : `./${pkg}`));
  }
  return [...grouped].map(([module, packages]) => [module, [...packages].join(" ")]);
}

function moduleOf(dir) {
  let current = dir;
  while (current !== root && current !== dirname(current)) {
    if (existsSync(join(current, "go.mod"))) return current;
    current = dirname(current);
  }
  return root;
}
