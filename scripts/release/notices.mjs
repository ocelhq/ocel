#!/usr/bin/env node

import { execFileSync, spawnSync } from "node:child_process";
import {
  closeSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  openSync,
  readdirSync,
  readFileSync,
  readSync,
  renameSync,
  rmSync,
  statSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { basename, dirname, join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const REPO_ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "..", "..");

export const GO_LICENSES = "github.com/google/go-licenses/v2@v2.0.1";

export const FIRST_PARTY = ["github.com/ocelhq/ocel", "ocel.dev"];

export const PERMISSIVE = new Set([
  "0BSD",
  "Apache-2.0",
  "BlueOak-1.0.0",
  "BSD-2-Clause",
  "BSD-3-Clause",
  "CC0-1.0",
  "ISC",
  "MIT",
  "MIT-0",
  "MPL-2.0",
  "Unicode-DFS-2016",
  "Unlicense",
  "Zlib",
]);

const APACHE_HEADER =
  "its LICENSE is the Apache-2.0 file header, which the classifier does not take for a license";

export const OVERRIDES = {
  "github.com/cyberphone/json-canonicalization": { license: "Apache-2.0", reason: APACHE_HEADER },
  "github.com/in-toto/attestation": { license: "Apache-2.0", reason: APACHE_HEADER },
  "github.com/in-toto/in-toto-golang": { license: "Apache-2.0", reason: APACHE_HEADER },
  "github.com/mattn/go-localereader": {
    license: "MIT",
    reason: "it ships no LICENSE file, and its README declares MIT",
  },
  "github.com/segmentio/asm": {
    license: "MIT-0",
    reason: "its LICENSE is MIT No Attribution, a text the classifier does not carry",
  },
};

const LICENSE_FILE = /^(licen[cs]e|copying|unlicense)([-._].*)?$/i;
const NOTICE_FILE = /^(notices?|third[-_]?party[-_]?notice\w*)([-._].*)?$/i;
const SCRIPT = /\.(c|m)?js$/;

export function parseExpression(expression) {
  const tokens = expression.match(/\(|\)|[^\s()]+/g) ?? [];
  let at = 0;
  const peek = () => tokens[at];
  const next = () => tokens[at++];
  function primary() {
    const token = next();
    if (token === undefined) throw new Error(`${expression} ends early`);
    if (token === "(") {
      const inner = or();
      if (next() !== ")") throw new Error(`${expression} leaves a parenthesis open`);
      return inner;
    }
    if (peek() === "WITH") {
      next();
      const exception = next();
      if (exception === undefined) throw new Error(`${expression} names no exception`);
      return { id: token.replace(/\+$/, ""), exception };
    }
    return { id: token.replace(/\+$/, "") };
  }
  function and() {
    const terms = [primary()];
    while (peek() === "AND") {
      next();
      terms.push(primary());
    }
    return terms.length === 1 ? terms[0] : { and: terms };
  }
  function or() {
    const terms = [and()];
    while (peek() === "OR") {
      next();
      terms.push(and());
    }
    return terms.length === 1 ? terms[0] : { or: terms };
  }
  const tree = or();
  if (at !== tokens.length)
    throw new Error(`${expression} has trailing ${tokens.slice(at).join(" ")}`);
  return tree;
}

export function permitted(expression, allowed = PERMISSIVE) {
  let tree;
  try {
    tree = parseExpression(expression);
  } catch {
    return false;
  }
  const walk = (node) => {
    if (node.or) return node.or.some(walk);
    if (node.and) return node.and.every(walk);
    return allowed.has(node.id);
  };
  return walk(tree);
}

export function declaredLicense(manifest) {
  const { license, licenses } = manifest;
  if (typeof license === "string") return license;
  if (license && typeof license.type === "string") return license.type;
  if (Array.isArray(licenses) && licenses.length > 0) {
    const ids = licenses.map((entry) => (typeof entry === "string" ? entry : entry.type));
    return ids.length === 1 ? ids[0] : `(${ids.join(" OR ")})`;
  }
  return undefined;
}

export function storeDirectory(input) {
  const path = input.replaceAll("\\", "/");
  const store = path.lastIndexOf("node_modules/.pnpm/");
  if (store === -1) return undefined;
  const inner = path.indexOf("/node_modules/", store + "node_modules/.pnpm/".length);
  if (inner === -1) return undefined;
  const rest = path.slice(inner + "/node_modules/".length).split("/");
  const name = rest[0].startsWith("@") ? `${rest[0]}/${rest[1]}` : rest[0];
  return `${path.slice(store, inner)}/node_modules/${name}`;
}

export function installedDirectory(path) {
  const match = /^(.*\/)?node_modules\/((@[^/]+\/)?[^/@.][^/]*)\/package\.json$/.exec(path);
  return match ? dirname(path) : undefined;
}

function run(command, args, options = {}) {
  return execFileSync(command, args, {
    encoding: "utf8",
    maxBuffer: 1 << 30,
    cwd: REPO_ROOT,
    ...options,
  });
}

function goLicensesBinary() {
  const bin = join(tmpdir(), `ocel-${GO_LICENSES.replaceAll(/[/@]/g, "_")}`);
  const binary = join(bin, "go-licenses");
  if (!existsSync(binary)) {
    mkdirSync(bin, { recursive: true });
    const staging = mkdtempSync(join(bin, "install-"));
    run("go", ["install", GO_LICENSES], {
      cwd: tmpdir(),
      env: { ...process.env, GOBIN: staging, GOWORK: "off", GOFLAGS: "" },
      stdio: ["ignore", "ignore", "inherit"],
    });
    renameSync(join(staging, "go-licenses"), binary);
    rmSync(staging, { recursive: true, force: true });
  }
  return binary;
}

export function parseBuildInfo(text) {
  const info = { tags: "" };
  for (const line of text.split("\n")) {
    const fields = line.split("\t");
    if (fields[1] === "path") info.path = fields[2];
    if (fields[1] === "build") {
      const [key, ...value] = fields[2].split("=");
      if (key === "GOOS") info.goos = value.join("=");
      if (key === "GOARCH") info.goarch = value.join("=");
      if (key === "-tags") info.tags = value.join("=");
    }
  }
  const [, goVersion] = /:\s+(go\S+)/.exec(text.split("\n")[0]) ?? [];
  info.goVersion = goVersion;
  return info.path && info.goos && info.goarch ? info : undefined;
}

function buildInfo(file) {
  const result = spawnSync("go", ["version", "-m", file], { encoding: "utf8" });
  return result.status === 0 ? parseBuildInfo(result.stdout) : undefined;
}

function executable(file) {
  const head = Buffer.alloc(4);
  const fd = openSync(file, "r");
  try {
    readSync(fd, head, 0, 4, 0);
  } finally {
    closeSync(fd);
  }
  const magic = head.toString("hex");
  return (
    magic === "7f454c46" || magic === "cffaedfe" || magic === "feedfacf" || magic.startsWith("4d5a")
  );
}

function goEnv(info) {
  return {
    ...process.env,
    GOOS: info.goos,
    GOARCH: info.goarch,
    CGO_ENABLED: "0",
    GOFLAGS: info.tags ? `-tags=${info.tags}` : "",
  };
}

function goPackages(info) {
  const out = run(
    "go",
    ["list", "-deps", "-json=ImportPath,Dir,EmbedFiles,Module,Standard", info.path],
    { env: goEnv(info) },
  );
  return JSON.parse(`[${out.replaceAll(/^}\n{/gm, "},{")}]`);
}

function ignored(file) {
  return spawnSync("git", ["check-ignore", "-q", file], { cwd: REPO_ROOT }).status === 0;
}

function firstParty(path) {
  return FIRST_PARTY.some((prefix) => path === prefix || path.startsWith(`${prefix}/`));
}

function licenseTexts(dir, pattern) {
  if (!dir || !existsSync(dir)) return [];
  return readdirSync(dir, { withFileTypes: true })
    .filter((entry) => entry.isFile() && pattern.test(entry.name))
    .map((entry) => entry.name)
    .sort()
    .map((name) => ({ name, text: readFileSync(join(dir, name), "utf8") }));
}

function goComponents(info, packages) {
  const template = join(mkdtempSync(join(tmpdir(), "ocel-notices-")), "report.tpl");
  writeFileSync(
    template,
    "{{range .}}{{.Name}}\t{{.Version}}\t{{.LicenseName}}\t{{.LicensePath}}\n{{end}}",
  );
  const args = ["report", info.path, "--template", template];
  for (const prefix of FIRST_PARTY) args.push("--ignore", prefix);
  const out = run(goLicensesBinary(), args, {
    env: goEnv(info),
    stdio: ["ignore", "pipe", "ignore"],
  });
  rmSync(dirname(template), { recursive: true, force: true });

  const modules = new Map();
  for (const pkg of packages) {
    if (pkg.Module) modules.set(pkg.Module.Path, pkg.Module);
  }
  const moduleOf = (name) =>
    [...modules.values()]
      .filter((module) => name === module.Path || name.startsWith(`${module.Path}/`))
      .sort((a, b) => b.Path.length - a.Path.length)[0];

  const libraries = new Map();
  for (const line of out.split("\n").filter(Boolean)) {
    const [name, version, licenseName, licensePath] = line.split("\t");
    const module = moduleOf(name);
    const library = libraries.get(name) ?? {
      name,
      version: module?.Version ?? version,
      module,
      names: new Set(),
      path: licensePath === "Unknown" ? undefined : licensePath,
    };
    library.names.add(licenseName);
    libraries.set(name, library);
  }

  const components = new Map();
  const problems = new Set();
  for (const library of libraries.values()) {
    const modulePath = library.module?.Path ?? library.name;
    const key = `go ${modulePath}@${library.version}`;
    if (library.names.has("Unknown")) {
      if (components.has(key)) continue;
      const dir = library.module?.Dir;
      const texts = licenseTexts(dir, LICENSE_FILE);
      const override = OVERRIDES[modulePath];
      if (!override) {
        problems.add(
          `${modulePath}@${library.version}: go-licenses cannot classify its license${
            texts.length > 0 ? ` in ${texts.map((entry) => entry.name).join(", ")}` : ""
          }`,
        );
        continue;
      }
      components.set(key, {
        ecosystem: "go",
        name: modulePath,
        version: library.version,
        license: override.license,
        texts,
        notices: licenseTexts(dir, NOTICE_FILE),
      });
      continue;
    }
    const license = [...library.names].sort().join(" AND ");
    const existing = components.get(key);
    const dir = library.path && dirname(library.path);
    const texts = library.path
      ? [{ name: basename(library.path), text: readFileSync(library.path, "utf8") }]
      : [];
    const component = existing ?? {
      ecosystem: "go",
      name: modulePath,
      version: library.version,
      license,
      texts: [],
      notices: [],
    };
    if (existing && existing.license !== license) {
      component.license = [...new Set([...existing.license.split(" AND "), ...library.names])]
        .sort()
        .join(" AND ");
    }
    for (const text of texts) {
      if (!component.texts.some((have) => have.text === text.text)) component.texts.push(text);
    }
    for (const notice of licenseTexts(dir, NOTICE_FILE)) {
      if (!component.notices.some((have) => have.text === notice.text)) {
        component.notices.push(notice);
      }
    }
    components.set(key, component);
  }
  return { components, problems: [...problems] };
}

function npmComponent(dir) {
  const manifest = JSON.parse(readFileSync(join(dir, "package.json"), "utf8"));
  return {
    ecosystem: "npm",
    name: manifest.name,
    version: manifest.version,
    license: declaredLicense(manifest) ?? OVERRIDES[manifest.name]?.license,
    texts: licenseTexts(dir, LICENSE_FILE),
    notices: licenseTexts(dir, NOTICE_FILE),
  };
}

function walk(dir) {
  return readdirSync(dir, { recursive: true, withFileTypes: true })
    .filter((entry) => entry.isFile())
    .map((entry) => join(entry.parentPath, entry.name))
    .sort();
}

export class Collector {
  constructor() {
    this.components = new Map();
    this.problems = [];
    this.toolchains = new Set();
    this.seen = new Set();
    this.scratch = mkdtempSync(join(tmpdir(), "ocel-notices-"));
  }

  add(component) {
    const key = `${component.ecosystem} ${component.name}@${component.version}`;
    if (!this.components.has(key)) this.components.set(key, component);
  }

  binary(file, info = buildInfo(file)) {
    if (!info) throw new Error(`${file} is not a Go binary go version -m can read`);
    const key = `${info.path} ${info.goos}/${info.goarch} ${info.tags}`;
    if (this.seen.has(key)) return info;
    this.seen.add(key);
    this.toolchains.add(info.goVersion);

    const packages = goPackages(info);
    const { components, problems } = goComponents(info, packages);
    for (const component of components.values()) this.add(component);
    for (const problem of problems) {
      if (!this.problems.includes(problem)) this.problems.push(problem);
    }

    for (const pkg of packages) {
      if (pkg.Standard || !pkg.EmbedFiles || !firstParty(pkg.Module?.Path ?? "")) continue;
      const roots = new Map();
      for (const file of pkg.EmbedFiles) {
        const [top] = file.split("/");
        const root = file.includes("/") ? join(pkg.Dir, top) : pkg.Dir;
        if (!roots.has(root)) roots.set(root, []);
        roots.get(root).push(join(pkg.Dir, file));
      }
      for (const [root, files] of roots) this.embedded(root, files);
    }
    return info;
  }

  embedded(root, files) {
    let scripts = false;
    for (const file of files) scripts = this.scan(file) || scripts;
    const records = join(root, ".bundles");
    if (!existsSync(records)) {
      if (scripts) {
        this.problems.push(
          `${relative(REPO_ROOT, root)} embeds scripts but holds no .bundles record of what they bundle`,
        );
      }
      return;
    }
    const inputs = new Set();
    for (const record of walk(records).filter((path) => path.endsWith(".json"))) {
      for (const input of Object.keys(JSON.parse(readFileSync(record, "utf8")).inputs ?? {})) {
        inputs.add(input);
      }
    }
    const directories = new Set();
    for (const input of inputs) {
      if (!input.includes("node_modules/")) continue;
      const store = storeDirectory(input);
      if (!store) {
        this.problems.push(
          `${input} bundled into ${relative(REPO_ROOT, root)} is outside the pnpm store`,
        );
        continue;
      }
      directories.add(store);
    }
    for (const store of [...directories].sort()) {
      const dir = join(REPO_ROOT, store);
      if (!existsSync(join(dir, "package.json"))) {
        this.problems.push(`${store} is recorded as bundled but is not installed`);
        continue;
      }
      this.add(npmComponent(dir));
    }
  }

  scan(file, generated) {
    if (file.endsWith(".zip")) {
      const into = mkdtempSync(join(this.scratch, "zip-"));
      run("unzip", ["-q", "-o", file, "-d", into]);
      let scripts = false;
      for (const entry of walk(into)) {
        const installed = installedDirectory(relative(into, entry));
        if (installed) {
          this.add(npmComponent(join(into, installed)));
          continue;
        }
        if (/(^|\/)node_modules\//.test(relative(into, entry))) continue;
        scripts = this.scan(entry, true) || scripts;
      }
      return scripts;
    }
    if (SCRIPT.test(file)) return generated ?? ignored(file);
    if (statSync(file).size >= 4 && executable(file)) {
      const info = buildInfo(file);
      if (info) this.binary(file, info);
    }
    return false;
  }

  dispose() {
    rmSync(this.scratch, { recursive: true, force: true });
  }
}

export function violations(components) {
  const found = [];
  for (const component of components) {
    const where = `${component.ecosystem} ${component.name}@${component.version}`;
    if (!component.license) {
      found.push(`${where}: declares no license`);
    } else if (!permitted(component.license)) {
      found.push(`${where}: ${component.license} is not on the permissive allowlist`);
    }
  }
  return found;
}

const RULE = "=".repeat(80);
const THIN = "-".repeat(80);

function compare(a, b) {
  return a < b ? -1 : a > b ? 1 : 0;
}

export function render({ binary, target, components, toolchains, goLicense, apacheLicense }) {
  const sorted = [...components].sort(
    (a, b) =>
      compare(a.ecosystem, b.ecosystem) ||
      compare(a.name, b.name) ||
      compare(String(a.version), String(b.version)),
  );
  const label = (c) => `${c.ecosystem} ${c.name}${c.version ? ` ${c.version}` : ""}`;
  const stdlib = [...toolchains].sort().map((version) => `go standard library ${version}`);
  const lines = [
    `Third-party notices for ${binary} (${target})`,
    "",
    `${binary} redistributes the components below. Each is listed with its license, and`,
    "the license and NOTICE texts it ships follow, grouped where the text is identical.",
    "",
    "Components",
    "",
    ...stdlib.map((name) => `  ${name}  BSD-3-Clause`),
    ...sorted.map((component) => `  ${label(component)}  ${component.license}`),
  ];

  const groups = new Map();
  if (stdlib.length > 0) groups.set(goLicense.trimEnd(), stdlib);
  const bare = [];
  for (const component of sorted) {
    if (component.texts.length === 0) {
      bare.push(component);
      continue;
    }
    const text = component.texts.map((entry) => entry.text.trimEnd()).join(`\n\n${THIN}\n\n`);
    if (!groups.has(text)) groups.set(text, []);
    groups.get(text).push(label(component));
  }

  lines.push("", "", RULE, "Licenses", RULE);
  for (const [text, names] of groups) lines.push("", THIN, ...names, THIN, "", text);

  if (bare.length > 0) {
    lines.push("", "", RULE, "Declared licenses, no license file shipped", RULE, "");
    for (const component of bare) lines.push(`  ${label(component)}  ${component.license}`);
  }

  const noticed = sorted.filter((component) => component.notices.length > 0);
  if (noticed.length > 0) {
    lines.push("", "", RULE, "NOTICE files", RULE);
    for (const component of noticed) {
      for (const notice of component.notices) {
        lines.push(
          "",
          THIN,
          `${label(component)}: ${notice.name}`,
          THIN,
          "",
          notice.text.trimEnd(),
        );
      }
    }
  }

  if (sorted.some((component) => /\bApache-2\.0\b/.test(component.license ?? ""))) {
    lines.push("", "", RULE, "Apache License, Version 2.0", RULE, "", apacheLicense.trimEnd());
  }
  return `${lines.join("\n")}\n`;
}

function main() {
  const [binary, output] = process.argv.slice(2);
  if (!binary || !output) {
    console.error("usage: notices.mjs <go binary> <notices file>");
    process.exit(1);
  }
  const collector = new Collector();
  let info;
  try {
    info = collector.binary(resolve(binary));
  } finally {
    collector.dispose();
  }
  const components = [...collector.components.values()];
  const failures = [...collector.problems, ...violations(components)];
  if (failures.length > 0) {
    console.error(`${basename(binary)} (${info.goos}/${info.goarch}) fails the license gate:`);
    for (const failure of failures) console.error(`  ${failure}`);
    process.exit(1);
  }
  const text = render({
    binary: basename(binary),
    target: `${info.goos}/${info.goarch}`,
    components,
    toolchains: collector.toolchains,
    goLicense: readFileSync(join(run("go", ["env", "GOROOT"]).trim(), "LICENSE"), "utf8"),
    apacheLicense: readFileSync(join(REPO_ROOT, "LICENSE"), "utf8"),
  });
  mkdirSync(dirname(resolve(output)), { recursive: true });
  writeFileSync(resolve(output), text);
}

if (import.meta.main) main();
