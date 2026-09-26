import { readFileSync } from "node:fs";
import { posix } from "node:path";

const contractFiles = [".greptile/rules.md", "AGENTS.md", "CLAUDE.md", "CONTRIBUTING.md"];

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

if (import.meta.main) {
  const pullRequest = {
    repository: process.env.REPOSITORY ?? "",
    headRepository: process.env.HEAD_REPOSITORY ?? "",
    association: process.env.ASSOCIATION ?? "",
  };
  const touched = readFileSync(0, "utf8").split("\n").filter(Boolean).filter(isContractPath);
  if (touched.length > 0 && !canChangeContract(pullRequest)) {
    console.error(
      `Only maintainers change contract paths (CONTRIBUTING.md), and this fork's author is ${pullRequest.association || "NONE"}:`,
    );
    for (const path of touched) console.error(`  ${path}`);
    process.exit(1);
  }
}
