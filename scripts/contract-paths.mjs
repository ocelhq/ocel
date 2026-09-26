import { readFileSync } from "node:fs";

const contract = [
  "pkg/provider/*.go",
  "platform/edge/contract/",
  "proto/",
  "packages/",
  "sdk/",
  "python/",
  "crates/",
  ".greptile/rules.md",
  "AGENTS.md",
  "CLAUDE.md",
  "CONTRIBUTING.md",
];

const maintainers = new Set(["OWNER", "MEMBER", "COLLABORATOR"]);

function covers(entry, path) {
  if (entry.endsWith("/")) return path.startsWith(entry);
  const [directory, extension] = entry.split("*");
  if (extension === undefined) return path === entry;
  const name = path.slice(directory.length);
  return path.startsWith(directory) && name.endsWith(extension) && !name.includes("/");
}

export function findContractPaths(paths) {
  return paths.filter((path) => contract.some((entry) => covers(entry, path)));
}

export function canChangeContract(association) {
  return maintainers.has(association);
}

if (import.meta.main) {
  const association = process.argv[2] ?? "";
  const touched = findContractPaths(readFileSync(0, "utf8").split("\n").filter(Boolean));
  if (touched.length > 0 && !canChangeContract(association)) {
    console.error(
      `Only maintainers change contract paths (CONTRIBUTING.md), and this author is ${association || "NONE"}:`,
    );
    for (const path of touched) console.error(`  ${path}`);
    process.exit(1);
  }
}
