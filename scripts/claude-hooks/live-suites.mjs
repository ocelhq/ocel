#!/usr/bin/env node

import { readFileSync } from "node:fs";
import { basename } from "node:path";
import { fileURLToPath } from "node:url";

const LIVE_SUITES = ["incus.sh", "incus-fanout.sh", "floci.sh", "act.sh"];
const RUNNERS = [
  "bash",
  "sh",
  "zsh",
  "exec",
  "env",
  "time",
  "nohup",
  "sudo",
  "timeout",
  "command",
  "nice",
  "source",
  ".",
];
const OPT_IN = "OCEL_LIVE_LOCAL=1";

function commandWord(segment) {
  const words = segment.trim().split(/\s+/).filter(Boolean);
  return words.find(
    (word) =>
      !RUNNERS.includes(word) &&
      !word.startsWith("-") &&
      !/^[A-Za-z_][A-Za-z0-9_]*=/.test(word) &&
      !/^\d+[smhd]?$/.test(word),
  );
}

export function findLiveSuite(command) {
  const words = command.replace(/["'`]/g, " ");
  if (words.split(/\s+/).includes(OPT_IN)) return undefined;
  for (const segment of words.split(/&&|\|\||\$\(|[;|&\n()]/)) {
    const word = commandWord(segment);
    if (word && LIVE_SUITES.includes(basename(word))) return basename(word);
  }
  return undefined;
}

function main() {
  if (process.env.OCEL_LIVE_LOCAL === "1") return;
  let command;
  try {
    command = JSON.parse(readFileSync(0, "utf8")).tool_input?.command;
  } catch {
    return;
  }
  if (typeof command !== "string") return;
  const script = findLiveSuite(command);
  if (!script) return;
  process.stderr.write(
    `scripts/${script} runs a live suite on VMs or containers, and CI already runs it on every push that needs it. ` +
      "Push the branch and let CI run it. " +
      "Set OCEL_LIVE_LOCAL=1 only to reproduce a live failure CI already reported.\n",
  );
  process.exitCode = 2;
}

if (process.argv[1] === fileURLToPath(import.meta.url)) main();
