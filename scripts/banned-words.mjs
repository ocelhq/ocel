import { execFileSync } from "node:child_process";
import { lstatSync, readFileSync } from "node:fs";

const bannedWords = [
  "standing",
  "substrate",
  "substrates",
  "held",
  "carried",
  "settle",
  "settled",
  "settles",
  "settling",
  "settlement",
  "unsettled",
  "owe",
  "owed",
  "owes",
  "owing",
];

const literalUses = [
  {
    use: "ECMAScript's settled promise",
    phrase: /\ballSettled\b|\bPromiseSettledResult\b|\bsettled(?:Within|Pool)\b/g,
  },
  {
    use: "a lock or a connection held",
    phrase: /\blocks?[ _]?held\b|\bheld (?:[\w']+ )?locks?\b|\bheld open\b/gi,
  },
  { use: "a persisted JSON tag", phrase: /json:"(?:owed|settled)(?=[,"])/g },
];

const excludedPaths = [
  "scripts/banned-words.mjs",
  "scripts/banned-words.test.mjs",
  "**/gen/**",
  "*/**/proto/**",
  "**/worker-configuration.d.ts",
  "**/*.lock",
  "**/*-lock.yaml",
  "**/*.sum",
  "**/LICENSE",
].map((pattern) => `:(exclude,glob)${pattern}`);

const capitalised = bannedWords.map((word) => word[0].toUpperCase() + word.slice(1));
const standalone = [...bannedWords, ...bannedWords.map((word) => word.toUpperCase())];
const bannedWord = new RegExp(
  `(?<![A-Za-z])(?:${standalone.join("|")})(?![a-z])|(?:${capitalised.join("|")})(?![a-z])`,
);

export function hasBannedWord(line) {
  const rest = literalUses.reduce((text, { phrase }) => text.replace(phrase, " "), line);
  return bannedWord.test(rest);
}

if (import.meta.main) {
  const paths = execFileSync("git", ["ls-files", "-z", "--", ".", ...excludedPaths], {
    encoding: "utf8",
    maxBuffer: 64 * 1024 * 1024,
  })
    .split("\0")
    .filter(Boolean);
  const hits = [];
  for (const path of paths) {
    if (hasBannedWord(path)) hits.push(path);
    if (!lstatSync(path, { throwIfNoEntry: false })?.isFile()) continue;
    const content = readFileSync(path);
    if (content.includes(0)) continue;
    for (const [index, line] of content.toString("utf8").split("\n").entries()) {
      if (hasBannedWord(line)) hits.push(`${path}:${index + 1}:${line}`);
    }
  }
  if (hits.length > 0) {
    console.log(hits.join("\n"));
    console.error(
      `banned words: name the actual state instead (.greptile/rules.md, Naming rule 9). Allowed literal uses: ${literalUses.map(({ use }) => use).join("; ")}.`,
    );
    process.exit(1);
  }
}
