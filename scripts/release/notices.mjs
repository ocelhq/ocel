#!/usr/bin/env node

import { execFileSync } from "node:child_process";
import { readdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const REPO_ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "..", "..");

const GOOS = ["darwin", "linux", "windows"];
const LICENSING = /^(licen[cs]e|copying|notice)(-[\w.-]+|\.(md|txt|markdown|rst))?$/i;
const DECLARED_BY_README = new Map([["github.com/mattn/go-localereader", "MIT"]]);
const MAX_BUFFER = 256 * 1024 * 1024;

const HEADER = `Third-party notices

The ocel, provider and connector binaries of an Ocel release are built from
the third-party software below. Each list of components ships under the
license text that follows it.
`;

export function licensing(dir) {
  return readdirSync(dir, { withFileTypes: true })
    .filter((entry) => entry.isFile() && LICENSING.test(entry.name))
    .map((entry) => join(dir, entry.name))
    .sort();
}

export function render(components) {
  const groups = new Map();
  for (const { name, version, license, text } of components) {
    const lines = groups.get(text) ?? new Set();
    lines.add(license ? `${name} ${version} (${license})` : `${name} ${version}`);
    groups.set(text, lines);
  }
  const sections = [...groups]
    .map(([text, lines]) => ({ text, lines: [...lines].sort(byCodePoint) }))
    .sort((a, b) => byCodePoint(a.lines[0], b.lines[0]));
  return [
    HEADER,
    ...sections.map(({ text, lines }) =>
      ["=".repeat(80), ...lines, "-".repeat(80), "", text, ""].join("\n"),
    ),
  ].join("\n");
}

function byCodePoint(a, b) {
  return a < b ? -1 : a > b ? 1 : 0;
}

function read(files) {
  return files
    .map((file) => readFileSync(file, "utf8").replaceAll("\r\n", "\n").trimEnd())
    .join("\n\n");
}

function undeclared(name, license, where) {
  return `${name} declares ${license} in ${where} and ships no license file.`;
}

function run(command, args, env = process.env) {
  return execFileSync(command, args, {
    cwd: REPO_ROOT,
    env,
    encoding: "utf8",
    maxBuffer: MAX_BUFFER,
    stdio: ["ignore", "pipe", "inherit"],
  });
}

function goComponents() {
  const patterns = JSON.parse(run("go", ["work", "edit", "-json"]))
    .Use.map((use) => use.DiskPath)
    .filter((path) => !/^\.\/(scripts|tests)\//.test(path))
    .map((path) => `${path}/...`);
  const modules = new Map();
  for (const goos of GOOS) {
    const listed = run(
      "go",
      [
        "list",
        "-deps",
        "-f",
        "{{with .Module}}{{.Path}}\t{{.Version}}\t{{.Dir}}{{end}}",
        ...patterns,
      ],
      { ...process.env, GOOS: goos },
    );
    for (const line of listed.split("\n").filter(Boolean)) {
      const [name, version, dir] = line.split("\t");
      if (!name.startsWith("github.com/ocelhq/ocel/"))
        modules.set(`${name} ${version}`, { name, version, dir });
    }
  }
  const unlicensed = [...modules.values()]
    .filter(({ name, dir }) => !licensing(dir).length && !DECLARED_BY_README.has(name))
    .map(({ name, version }) => `${name} ${version}`);
  if (unlicensed.length) throw new Error(`no license file in ${unlicensed.join(", ")}`);
  return [
    {
      name: "Go standard library",
      version: run("go", ["env", "GOVERSION"]).trim(),
      license: "BSD-3-Clause",
      text: read([join(run("go", ["env", "GOROOT"]).trim(), "LICENSE")]),
    },
    ...[...modules.values()].map(({ name, version, dir }) => {
      const files = licensing(dir);
      if (files.length) return { name, version, text: read(files) };
      const license = DECLARED_BY_README.get(name);
      return { name, version, license, text: undeclared(name, license, "its README") };
    }),
  ];
}

function embedsWithGo(dir) {
  return readdirSync(dir)
    .filter((name) => name.endsWith(".go") && !name.endsWith("_test.go"))
    .some((name) => /^\/\/go:embed /m.test(readFileSync(join(dir, name), "utf8")));
}

function jsComponents() {
  const embedded = JSON.parse(run("pnpm", ["ls", "--recursive", "--depth", "-1", "--json"]))
    .filter((project) => embedsWithGo(project.path))
    .flatMap((project) => ["--filter", `${project.name}...`]);
  if (embedded.length === 0) throw new Error("no workspace package is embedded by Go");
  const listed = JSON.parse(run("pnpm", ["licenses", "list", "--json", ...embedded]));
  return Object.values(listed)
    .flat()
    .flatMap((pkg) =>
      pkg.versions.map((version, index) => {
        if (!pkg.license || pkg.license === "Unknown") {
          throw new Error(`${pkg.name} ${version} declares no license`);
        }
        const files = licensing(pkg.paths[index]);
        return {
          name: pkg.name,
          version,
          license: pkg.license,
          text: files.length ? read(files) : undeclared(pkg.name, pkg.license, "its package.json"),
        };
      }),
    );
}

function main() {
  const [out] = process.argv.slice(2);
  if (!out) {
    console.error("usage: notices.mjs <output file>");
    process.exit(1);
  }
  writeFileSync(out, render([...goComponents(), ...jsComponents()]));
}

if (import.meta.main) main();
