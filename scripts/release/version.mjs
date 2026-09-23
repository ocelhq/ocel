#!/usr/bin/env node

import { execFileSync } from "node:child_process";
import { existsSync, readdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const REPO_ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "..", "..");

const CORE = String.raw`(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)`;
const STABLE = new RegExp(`^${CORE}$`);
const RC = new RegExp(`^${CORE}-rc\\.([1-9]\\d*)$`);
const NIGHTLY = new RegExp(`^${CORE}-0\\.nightly\\.(\\d{8})\\.g([0-9a-f]{7})$`);

const SCHEMA_URL = /https:\/\/ocel\.dev\/schema\/[0-9A-Za-z.+-]+\/ocel\.schema\.json/g;

export function parse(version) {
  const stable = STABLE.exec(version);
  if (stable) return { channel: "stable", base: version };
  const rc = RC.exec(version);
  if (rc) return { channel: "rc", base: rc.slice(1, 4).join("."), rc: Number(rc[4]) };
  const nightly = NIGHTLY.exec(version);
  if (nightly) {
    return {
      channel: "nightly",
      base: nightly.slice(1, 4).join("."),
      date: nightly[4],
      sha: nightly[5],
    };
  }
  throw new Error(
    `${JSON.stringify(version)} is none of X.Y.Z, X.Y.Z-rc.N or X.Y.Z-0.nightly.YYYYMMDD.g<sha7>`,
  );
}

export function pep440(version) {
  const parsed = parse(version);
  switch (parsed.channel) {
    case "rc":
      return `${parsed.base}rc${parsed.rc}`;
    case "nightly":
      return `${parsed.base}.dev${parsed.date}`;
    default:
      return parsed.base;
  }
}

export function schemaURL(version) {
  return `https://ocel.dev/schema/${version}/ocel.schema.json`;
}

export function withSection(text, header, edit) {
  const lines = text.split("\n");
  const start = lines.indexOf(header);
  if (start === -1) throw new Error(`no ${header} section`);
  const next = lines.findIndex((line, index) => index > start && line.startsWith("["));
  const end = next === -1 ? lines.length : next;
  const body = edit(lines.slice(start + 1, end).join("\n"));
  return [...lines.slice(0, start + 1), body, ...lines.slice(end)].join("\n");
}

export function withVersionLine(section, version) {
  if (!/^version = "[^"]*"$/m.test(section)) throw new Error("the section names no version");
  return section.replace(/^version = "[^"]*"$/m, `version = "${version}"`);
}

export function withPathPins(manifest, version) {
  return manifest.replace(/\{[^{}\n]*\bpath = "\.\.\/[^"]+"[^{}\n]*\}/g, (table) =>
    table.replace(/\bversion = "[^"]*"/, `version = "${version}"`),
  );
}

export function withSchemaURLs(text, version) {
  return text.replace(SCHEMA_URL, schemaURL(version));
}

function edit(path, change) {
  const before = readFileSync(path, "utf8");
  const after = change(before);
  if (after !== before) writeFileSync(path, after);
}

function members(dir, manifest) {
  return readdirSync(dir, { withFileTypes: true })
    .filter((entry) => entry.isDirectory())
    .map((entry) => join(dir, entry.name, manifest))
    .filter((path) => existsSync(path));
}

function run(command, args, cwd = REPO_ROOT) {
  execFileSync(command, args, { cwd, stdio: ["ignore", 2, "inherit"] });
}

function tracked(...pathspecs) {
  return execFileSync("git", ["ls-files", "-z", "--", ...pathspecs], {
    cwd: REPO_ROOT,
    encoding: "utf8",
  })
    .split("\0")
    .filter(Boolean);
}

function stampNpm(version) {
  for (const path of members(join(REPO_ROOT, "packages"), "package.json")) {
    const manifest = JSON.parse(readFileSync(path, "utf8"));
    manifest.version = version;
    writeFileSync(path, `${JSON.stringify(manifest, null, 2)}\n`);
  }
}

function stampPython(version) {
  for (const path of members(join(REPO_ROOT, "python"), "pyproject.toml")) {
    edit(path, (text) =>
      withSection(text, "[project]", (section) => withVersionLine(section, pep440(version))),
    );
  }
}

function stampCrates(version) {
  const crates = join(REPO_ROOT, "crates");
  edit(join(crates, "Cargo.toml"), (text) =>
    withSection(text, "[workspace.package]", (section) => withVersionLine(section, version)),
  );
  for (const path of members(crates, "Cargo.toml")) {
    edit(path, (text) => withPathPins(text, version));
  }
}

function stampSchemaURLs(version) {
  for (const path of tracked(":!www/public/schema/")) {
    const absolute = join(REPO_ROOT, path);
    let text;
    try {
      text = readFileSync(absolute, "utf8");
    } catch {
      continue;
    }
    if (text.includes("https://ocel.dev/schema/")) {
      edit(absolute, (current) => withSchemaURLs(current, version));
    }
  }
}

function refreshLockfiles() {
  run("pnpm", ["install", "--lockfile-only"]);
  run("uv", ["lock"], join(REPO_ROOT, "python"));
  for (const lockfile of tracked("*Cargo.lock")) {
    run("cargo", ["update", "--workspace"], join(REPO_ROOT, dirname(lockfile)));
  }
}

function main() {
  const [version] = process.argv.slice(2);
  if (!version) {
    console.error("usage: version.mjs <version>");
    process.exit(1);
  }
  try {
    parse(version);
  } catch (error) {
    console.error(error.message);
    process.exit(1);
  }
  writeFileSync(join(REPO_ROOT, "VERSION"), `${version}\n`);
  stampNpm(version);
  stampPython(version);
  stampCrates(version);
  stampSchemaURLs(version);
  run("node", [join(REPO_ROOT, "scripts", "schema", "build.mjs")]);
  refreshLockfiles();
}

if (import.meta.main) main();
