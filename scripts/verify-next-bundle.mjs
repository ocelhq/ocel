#!/usr/bin/env node

import { spawnSync } from "node:child_process";
import { existsSync, lstatSync, readFileSync, readdirSync, rmSync } from "node:fs";
import { basename, dirname, join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const repoRoot = dirname(dirname(fileURLToPath(import.meta.url)));

const opts = parseArgs(process.argv.slice(2));
const appDir = resolve(repoRoot, opts.app ?? "examples/next-test");
const appName = opts["app-name"] ?? basename(appDir);
const outRoot = resolve(repoRoot, opts.out ?? join(appDir, ".ocel/verify-output"));
const appOut = join(outRoot, "apps", appName);

const failures = [];
const notes = [];
const check = (ok, message) => {
  if (!ok) failures.push(message);
  return ok;
};

function main() {
  if (!opts["skip-adapter"] && !opts["skip-build"]) buildAdapter();
  if (!opts["skip-build"]) buildApp();
  verify();
  report();
}

function buildAdapter() {
  log("building @ocel/next-runtime");
  run("pnpm", ["--filter", "@ocel/next-runtime", "build"], repoRoot);
}

function buildApp() {
  const adapter = join(repoRoot, "packages/next-runtime/dist/next-adapter.mjs");
  if (!existsSync(adapter)) {
    fatal(`no built adapter at ${adapter} — drop --skip-adapter or run \`pnpm --filter @ocel/next-runtime build\``);
  }
  rmSync(outRoot, { recursive: true, force: true });
  log(`building ${relative(repoRoot, appDir)} into ${relative(repoRoot, appOut)}`);
  run("pnpm", ["run", "build"], appDir, {
    NEXT_ADAPTER_PATH: adapter,
    OCEL_APP_NAME: appName,
    OCEL_OUTPUT_DIR: appOut,
  });
}

function verify() {
  const functionsDir = join(appOut, "functions");
  if (!existsSync(functionsDir)) fatal(`no functions directory at ${functionsDir}`);

  const funcDirs = findFuncDirs(functionsDir);
  const symlinked = funcDirs.filter((d) => lstatSync(d).isSymbolicLink());
  check(
    symlinked.length === 0,
    `symlinked .func directories under functions/: ${symlinked.map((d) => relative(appOut, d)).join(", ")}`,
  );

  const manifestPath = join(appOut, "routing-manifest.json");
  if (!existsSync(manifestPath)) fatal(`no routing manifest at ${manifestPath}`);
  const manifest = JSON.parse(readFileSync(manifestPath, "utf8"));
  const dispatch = manifest.dispatch ?? {};

  const lambdaEntries = Object.entries(dispatch).filter(([, v]) => v.kind === "lambda");
  check(lambdaEntries.length > 1, `app routes to ${lambdaEntries.length} lambda pathname(s) — not a multi-route app, so "one bundle" proves nothing`);

  check(
    funcDirs.length === 1 && basename(funcDirs[0] ?? "") === "bundle-0.func",
    `expected exactly one functions/bundle-0.func, got [${funcDirs.map((d) => relative(functionsDir, d)).join(", ")}]`,
  );

  const bundles = new Map();
  for (const dir of funcDirs) {
    const name = basename(dir).replace(/\.func$/, "");
    const bundle = readBundle(dir, name);
    if (bundle) bundles.set(name, bundle);
  }
  if (bundles.size === 0) fatal("no readable bundles emitted");

  for (const bundle of bundles.values()) verifyBundle(bundle);
  verifyDispatch(dispatch, bundles);
  reportSize(functionsDir, bundles);
}

function readBundle(dir, name) {
  const configPath = join(dir, "config.json");
  if (!existsSync(configPath)) {
    failures.push(`${name}: no config.json`);
    return null;
  }
  const config = JSON.parse(readFileSync(configPath, "utf8"));
  const launcherRel = config.handler;
  const launcher = join(dir, launcherRel ?? "");
  if (!launcherRel || !existsSync(launcher)) {
    failures.push(`${name}: config.handler "${launcherRel}" names no file in the bundle`);
    return null;
  }
  const probe = probeLauncher(launcher);
  if (probe.error) {
    failures.push(`${name}: requiring the emitted launcher failed — ${probe.error}`);
    return null;
  }
  return { name, dir, config, launcher, launcherRel, ...probe };
}

function verifyBundle({ name, dir, config, launcher, launcherRel, entries, primary, unresolved, dispatchProbe }) {
  check(config.id === name, `${name}: config.json.id is ${JSON.stringify(config.id)}, expected ${JSON.stringify(name)}`);
  check(config.framework === "next", `${name}: config.json.framework is ${JSON.stringify(config.framework)}`);

  const appRelDir = dirname(launcherRel);
  check(
    existsSync(join(dir, appRelDir, "__ocel_dispatch.cjs")),
    `${name}: no ${join(appRelDir, "__ocel_dispatch.cjs")} beside the launcher`,
  );

  const keys = Object.keys(entries);
  check(keys.length > 0, `${name}: launcher ENTRIES is empty`);
  check(
    unresolved.length === 0,
    `${name}: launcher entries do not resolve from ${relative(dir, launcher)}: ${unresolved.map(([k, s]) => `${k} -> ${s}`).join(", ")}`,
  );
  check(
    typeof primary === "string" && Object.hasOwn(entries, primary),
    `${name}: launcher PRIMARY ${JSON.stringify(primary)} is not a key in ENTRIES`,
  );

  check(dispatchProbe.noHeader === 502, `${name}: dispatcher answered ${dispatchProbe.noHeader} for a request with no x-ocel-entry, expected 502`);
  check(dispatchProbe.unknownKey === 502, `${name}: dispatcher answered ${dispatchProbe.unknownKey} for an unknown entry key, expected 502`);
  check(dispatchProbe.routed === true, `${name}: dispatcher did not route a known entry key to its handler`);
}

function verifyDispatch(dispatch, bundles) {
  for (const [pathname, entry] of Object.entries(dispatch)) {
    const where = `${entry.kind} ${pathname}`;

    const nodeParented =
      typeof entry.entryKey === "string" || bundles.has(entry.id);

    if (entry.kind === "prerender" && !nodeParented) {
      check(
        typeof entry.edgeEntryKey === "string",
        `${where}: prerender names neither an entryKey nor an edgeEntryKey, so nothing can regenerate it`,
      );
      continue;
    }
    if (entry.kind !== "lambda" && entry.kind !== "prerender") continue;

    if (!check(typeof entry.id === "string", `${where}: no id`)) continue;
    if (!check(typeof entry.entryKey === "string", `${where}: no entryKey`)) continue;
    if (entry.kind === "prerender") {
      check(
        entry.edgeEntryKey === undefined,
        `${where}: node-parented prerender carries edgeEntryKey ${JSON.stringify(entry.edgeEntryKey)} — the worker reads that as "cannot revalidate"`,
      );
    }

    const bundle = bundles.get(entry.id);
    if (!check(bundle !== undefined, `${where}: id ${JSON.stringify(entry.id)} names no emitted bundle`)) continue;
    check(
      Object.hasOwn(bundle.entries, entry.entryKey),
      `${where}: entryKey ${JSON.stringify(entry.entryKey)} is not in ${entry.id}'s launcher ENTRIES`,
    );
  }
}

const probeSource = `
const Module = require("node:module");
const launcher = process.env.OCEL_PROBE_LAUNCHER;

let captured = null;
const realLoad = Module._load;
Module._load = function (request, ...rest) {
  if (request.endsWith("__ocel_dispatch.cjs")) {
    return (options) => {
      captured = options;
      return { handler() {} };
    };
  }
  return realLoad.call(this, request, ...rest);
};
try {
  require(launcher);
} finally {
  Module._load = realLoad;
}
if (!captured) throw new Error("the launcher never called the dispatcher factory");

const req = Module.createRequire(launcher);
const unresolved = Object.entries(captured.entries).filter(([, spec]) => {
  try {
    req.resolve(spec);
    return false;
  } catch {
    return true;
  }
});

// The bundle's own dispatcher copy, with a stub load in place of require.
const createDispatch = req("./__ocel_dispatch.cjs");
const keys = Object.keys(captured.entries);
const loadedSpecifiers = [];
const dispatcher = createDispatch({
  entries: captured.entries,
  primary: null,
  load: (specifier) => {
    loadedSpecifiers.push(specifier);
    return { handler: (_req, res) => { res.statusCode = 200; res.routed = true; } };
  },
});
const call = (headers) => {
  const res = { statusCode: 0, end() {} };
  dispatcher.handler({ headers }, res);
  return res;
};
const routedRes = keys.length > 0 ? call({ "x-ocel-entry": keys[0] }) : {};

process.stdout.write(JSON.stringify({
  entries: captured.entries,
  primary: captured.primary,
  unresolved,
  dispatchProbe: {
    noHeader: call({}).statusCode,
    unknownKey: call({ "x-ocel-entry": "/nope/not/an/entry" }).statusCode,
    routed: routedRes.routed === true && loadedSpecifiers[0] === captured.entries[keys[0]],
  },
}));
`;

function probeLauncher(launcher) {
  const res = spawnSync(process.execPath, ["-e", probeSource], {
    cwd: dirname(launcher),
    env: { ...process.env, OCEL_PROBE_LAUNCHER: launcher },
    encoding: "utf8",
  });
  if (res.status !== 0) {
    return { error: (res.stderr || res.error?.message || "").trim().split("\n").slice(0, 6).join(" | ") };
  }
  try {
    return JSON.parse(res.stdout);
  } catch {
    return { error: `probe printed unparseable output: ${res.stdout.slice(0, 200)}` };
  }
}

function reportSize(functionsDir, bundles) {
  const total = measure(functionsDir);
  notes.push(`bundled: ${bundles.size} function(s), ${total.files} files, ${mib(total.bytes)}`);
  for (const bundle of bundles.values()) {
    const one = measure(bundle.dir);
    notes.push(`  ${bundle.name}: ${Object.keys(bundle.entries).length} entries, ${one.files} files, ${mib(one.bytes)}, primary ${bundle.primary}`);
  }
  if (!opts.compare) return;

  const legacyDir = resolve(repoRoot, opts.compare);
  if (!existsSync(legacyDir)) {
    notes.push(`compare: ${legacyDir} does not exist — skipped`);
    return;
  }
  const legacyFuncs = findFuncDirs(legacyDir).filter((d) => !lstatSync(d).isSymbolicLink());
  const legacy = measure(legacyDir);
  notes.push(
    `legacy (${relative(repoRoot, legacyDir)}): ${legacyFuncs.length} function(s), ${legacy.files} files, ${mib(legacy.bytes)}` +
      ` — reporting only; a legacy tree may predate unrelated asset-copy changes, so compare cautiously`,
  );
}

function measure(dir) {
  let bytes = 0;
  let files = 0;
  const walk = (path) => {
    const info = lstatSync(path);
    if (info.isDirectory()) {
      for (const entry of readdirSync(path)) walk(join(path, entry));
      return;
    }
    bytes += info.size;
    files += 1;
  };
  walk(dir);
  return { bytes, files };
}

function findFuncDirs(root) {
  const found = [];
  const walk = (path) => {
    for (const entry of readdirSync(path, { withFileTypes: true })) {
      const child = join(path, entry.name);
      if (entry.name.endsWith(".func")) {
        found.push(child);
        continue;
      }
      if (entry.isDirectory()) walk(child);
    }
  };
  walk(root);
  return found.sort();
}

function report() {
  for (const note of notes) log(note);
  if (failures.length === 0) {
    log(`OK — ${relative(repoRoot, appOut)} is a valid bundled output tree`);
    return;
  }
  process.stderr.write(`\nverify-next-bundle: ${failures.length} assertion(s) failed\n`);
  for (const failure of failures) process.stderr.write(`  ✗ ${failure}\n`);
  process.exit(1);
}

function parseArgs(argv) {
  const flags = { "skip-build": false, "skip-adapter": false };
  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i];
    if (!arg.startsWith("--")) fatal(`unexpected argument ${arg}`);
    const name = arg.slice(2);
    if (name === "skip-build" || name === "skip-adapter") flags[name] = true;
    else if (i + 1 < argv.length) flags[name] = argv[++i];
    else fatal(`--${name} needs a value`);
  }
  return flags;
}

function run(command, args, cwd, env) {
  const res = spawnSync(command, args, { cwd, env: { ...process.env, ...env }, stdio: "inherit" });
  if (res.error) fatal(`${command} ${args.join(" ")}: ${res.error.message}`);
  if (res.status !== 0) fatal(`${command} ${args.join(" ")} exited with ${res.status}`);
}

function mib(bytes) {
  return `${(bytes / 1024 / 1024).toFixed(1)} MiB`;
}

function log(message) {
  process.stderr.write(`verify-next-bundle: ${message}\n`);
}

function fatal(message) {
  process.stderr.write(`verify-next-bundle: ${message}\n`);
  process.exit(1);
}

main();
