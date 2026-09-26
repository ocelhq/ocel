import { readFileSync } from "node:fs";
import { posix } from "node:path";

const contractFiles = [
  ".github/workflows/contract-paths.yml",
  ".greptile/rules.md",
  "AGENTS.md",
  "CLAUDE.md",
  "CONTRIBUTING.md",
  "scripts/contract-paths.mjs",
];

const contractDirectories = [
  "crates/",
  "packages/",
  "platform/edge/contract/",
  "proto/",
  "python/",
  "sdk/",
];

const contractGoPackages = ["pkg/provider"];

const maintainers = new Set(["OWNER", "MEMBER", "COLLABORATOR"]);

export function isContractPath(path) {
  return (
    contractFiles.includes(path) ||
    contractDirectories.some((directory) => path.startsWith(directory)) ||
    (path.endsWith(".go") && contractGoPackages.includes(posix.dirname(path)))
  );
}

export function canChangeContract({ repository, headRepository, association }) {
  return headRepository === repository || maintainers.has(association);
}

function listChangedPaths(files) {
  return files.flatMap((file) => [file.filename, file.previous_filename].filter(Boolean));
}

function fail(...lines) {
  for (const line of lines) console.error(line);
  process.exit(1);
}

if (import.meta.main) {
  const pullRequest = {
    repository: process.env.REPOSITORY ?? "",
    headRepository: process.env.HEAD_REPOSITORY ?? "",
    association: process.env.ASSOCIATION ?? "",
  };
  const files = readFileSync(0, "utf8")
    .split("\n")
    .filter(Boolean)
    .map((line) => JSON.parse(line));
  const changedFiles = Number(process.env.CHANGED_FILES);
  if (!(files.length >= changedFiles)) {
    fail(
      `GitHub listed ${files.length} of ${changedFiles} changed files, and this check cannot see the rest.`,
    );
  }
  const touched = listChangedPaths(files).filter(isContractPath);
  if (touched.length > 0 && !canChangeContract(pullRequest)) {
    fail(
      `Only maintainers change contract paths (CONTRIBUTING.md), and this fork's author is ${pullRequest.association || "NONE"}:`,
      ...touched.map((path) => `  ${path}`),
    );
  }
}
