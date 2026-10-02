import { spawn } from "node:child_process";
import { access, cp, readdir, readFile, rm, symlink, writeFile } from "node:fs/promises";
import path from "node:path";
import { outputRoot, repoRoot } from "./paths";

const NEVER_COPIED = [
  ".git",
  ".next",
  ".ocel",
  ".venv",
  "dist",
  "node_modules",
  "output",
  "target",
];
const NEVER_COPIED_FROM_A_PACKAGE = NEVER_COPIED.filter((name) => name !== "dist");

const WORKSPACE_FILE = "pnpm-workspace.yaml";
const GO_MODULE_FILE = "go.mod";
const GO_WORKSPACE_FILE = "go.work";
const PYTHON_PROJECT_FILE = "pyproject.toml";
const SDK_DIR_OF_PROJECT_FILE: [string, string][] = [
  [GO_MODULE_FILE, "sdk"],
  ["Cargo.toml", "crates"],
  [PYTHON_PROJECT_FILE, "python"],
];
const LOCKFILE = "pnpm-lock.yaml";
const MANIFEST = "package.json";
const DEPENDENCY_FIELDS = [
  "dependencies",
  "devDependencies",
  "optionalDependencies",
  "peerDependencies",
] as const;

export type Manifest = {
  name?: string;
  devEngines?: { packageManager?: { name?: string; version?: string } };
} & Partial<Record<(typeof DEPENDENCY_FIELDS)[number], Record<string, string>>>;

export type WorkspaceFile = { packages: string[]; settings: string };

export function splitWorkspaceFile(text: string): WorkspaceFile {
  const packages: string[] = [];
  const settings: string[] = [];
  let inPackages = false;
  for (const line of text.split("\n")) {
    const key = /^([A-Za-z][\w-]*):/.exec(line);
    if (key) {
      inPackages = key[1] === "packages";
      if (!inPackages) {
        settings.push(line);
      }
      continue;
    }
    if (!inPackages) {
      settings.push(line);
      continue;
    }
    const entry = /^\s*-\s*(.+?)\s*$/.exec(line);
    if (entry?.[1]) {
      packages.push(entry[1].replace(/^['"]|['"]$/g, ""));
    }
  }
  return { packages, settings: settings.join("\n").replace(/^\n+/, "") };
}

export function workspaceFileFor(members: string[], settings: string): string {
  const listed = members.map((member) => `  - ${member}\n`).join("");
  return `packages:\n${listed}${settings === "" ? "" : `\n${settings}`}`;
}

async function readManifest(file: string): Promise<Manifest | undefined> {
  try {
    return JSON.parse(await readFile(file, "utf8")) as Manifest;
  } catch {
    return undefined;
  }
}

async function expand(root: string, pattern: string): Promise<string[]> {
  let found = [""];
  for (const segment of pattern.split("/")) {
    const next: string[] = [];
    for (const prefix of found) {
      if (segment !== "*") {
        next.push(path.posix.join(prefix, segment));
        continue;
      }
      const entries = await readdir(path.join(root, prefix), { withFileTypes: true }).catch(
        () => [],
      );
      for (const entry of entries) {
        if (entry.isDirectory() && !entry.name.startsWith(".") && entry.name !== "node_modules") {
          next.push(path.posix.join(prefix, entry.name));
        }
      }
    }
    found = next;
  }
  return found;
}

export async function workspacePackages(root: string): Promise<Map<string, string>> {
  const file = await readFile(path.join(root, WORKSPACE_FILE), "utf8");
  const named = new Map<string, string>();
  for (const pattern of splitWorkspaceFile(file).packages) {
    for (const dir of await expand(root, pattern)) {
      const manifest = await readManifest(path.join(root, dir, MANIFEST));
      if (manifest?.name) {
        named.set(manifest.name, dir);
      }
    }
  }
  return named;
}

function under(dir: string, parent: string): boolean {
  return dir.startsWith(`${parent}/`);
}

export async function nestedMembers(root: string, appDirs: string[]): Promise<string[]> {
  const named = await workspacePackages(root);
  const nested = [...named.values()].filter((dir) => appDirs.some((app) => under(dir, app)));
  return nested.sort();
}

export async function workspaceClosure(root: string, appDirs: string[]): Promise<string[]> {
  const named = await workspacePackages(root);
  const reached = new Set<string>();
  const pending = [...appDirs];
  while (pending.length > 0) {
    const dir = pending.pop() as string;
    const manifest = await readManifest(path.join(root, dir, MANIFEST));
    if (!manifest) {
      continue;
    }
    for (const field of DEPENDENCY_FIELDS) {
      for (const [name, range] of Object.entries(manifest[field] ?? {})) {
        const where = named.get(name);
        if (!range.startsWith("workspace:") || where === undefined || reached.has(where)) {
          continue;
        }
        if (appDirs.includes(where)) {
          continue;
        }
        reached.add(where);
        pending.push(where);
      }
    }
  }
  return [...reached].sort();
}

function packageManagerOf(manifest: Manifest): string | undefined {
  const declared = manifest.devEngines?.packageManager;
  if (!declared?.name || !declared.version) {
    return undefined;
  }
  return `${declared.name}@${declared.version}`;
}

export function rootManifest(name: string, repoManifest: Manifest): string {
  const packageManager = packageManagerOf(repoManifest);
  const declared = Object.fromEntries(
    DEPENDENCY_FIELDS.flatMap((field) => {
      const ranges = repoManifest[field];
      return ranges === undefined ? [] : [[field, ranges] as const];
    }),
  );
  return `${JSON.stringify(
    {
      name,
      private: true,
      type: "module",
      ...(packageManager ? { packageManager } : {}),
      ...declared,
    },
    null,
    2,
  )}\n`;
}

export async function writeWorkspace(root: string, name: string, members: string[]): Promise<void> {
  const repoWorkspace = splitWorkspaceFile(
    await readFile(path.join(repoRoot, WORKSPACE_FILE), "utf8"),
  );
  await writeFile(
    path.join(root, WORKSPACE_FILE),
    workspaceFileFor(members, repoWorkspace.settings),
  );
  await writeFile(
    path.join(root, MANIFEST),
    rootManifest(name, (await readManifest(path.join(repoRoot, MANIFEST))) ?? {}),
  );
}

export function lockfileInstallArgs(pid: number): string[] {
  return [
    "install",
    "--lockfile-only",
    "--ignore-scripts",
    "--prefer-offline",
    "--cache-dir",
    path.join(outputRoot, "pnpm-cache", String(pid)),
  ];
}

export async function writeLockfile(root: string): Promise<void> {
  await cp(path.join(repoRoot, LOCKFILE), path.join(root, LOCKFILE));
  await run("pnpm", lockfileInstallArgs(process.pid), root);
}

async function linkVendored(source: string, dest: string): Promise<void> {
  const vendored = path.join(source, "node_modules");
  if (await exists(vendored)) {
    await symlink(vendored, path.join(dest, "node_modules"), "dir");
  }
}

async function copyInto(source: string, dest: string, never: string[]): Promise<string> {
  const skipped = new Set(never);
  await rm(dest, { recursive: true, force: true });
  await cp(source, dest, {
    recursive: true,
    filter: (from) => !skipped.has(path.basename(from)),
  });
  await linkVendored(source, dest);
  return dest;
}

export async function copyTree(source: string, dest: string): Promise<string> {
  return copyInto(source, dest, NEVER_COPIED);
}

export function formatGoWork(modules: string[], firstGoMod: string): string {
  const version = /^go\s+(\S+)\s*$/m.exec(firstGoMod)?.[1];
  if (!version) {
    throw new Error(`${modules[0]}/${GO_MODULE_FILE} names no go version`);
  }
  const used = modules.map((dir) => `\t./${dir}\n`).join("");
  return `go ${version}\n\nuse (\n${used})\n`;
}

async function writeGoWorkspace(root: string, apps: string[]): Promise<void> {
  const modules: string[] = [];
  for (const app of apps) {
    if (await exists(path.join(root, app, GO_MODULE_FILE))) {
      modules.push(app);
    }
  }
  const [first] = modules;
  if (first === undefined) {
    return;
  }
  const goMod = await readFile(path.join(root, first, GO_MODULE_FILE), "utf8");
  await writeFile(path.join(root, GO_WORKSPACE_FILE), formatGoWork(modules, goMod));
}

function exists(file: string): Promise<boolean> {
  return access(file).then(
    () => true,
    () => false,
  );
}

export async function readSdkDirs(appDir: string): Promise<string[]> {
  const dirs: string[] = [];
  for (const [projectFile, sdkDir] of SDK_DIR_OF_PROJECT_FILE) {
    if (await exists(path.join(appDir, projectFile))) {
      dirs.push(sdkDir);
    }
  }
  return dirs;
}

async function linkSdks(root: string, apps: string[]): Promise<void> {
  const linked = new Set<string>();
  for (const app of apps) {
    for (const sdkDir of await readSdkDirs(path.join(root, app))) {
      if (!linked.has(sdkDir)) {
        linked.add(sdkDir);
        await symlink(path.join(repoRoot, sdkDir), path.join(root, sdkDir), "dir");
      }
    }
  }
}

async function run(command: string, args: string[], cwd: string): Promise<void> {
  const said = await new Promise<{ code: number | null; output: string }>((resolve, reject) => {
    const child = spawn(command, args, { cwd, env: process.env });
    let output = "";
    child.stdout.on("data", (chunk) => {
      output += String(chunk);
    });
    child.stderr.on("data", (chunk) => {
      output += String(chunk);
    });
    child.on("error", reject);
    child.on("close", (code) => resolve({ code, output }));
  });
  if (said.code !== 0) {
    throw new Error(`${command} ${args.join(" ")} in ${cwd} exited ${said.code}\n${said.output}`);
  }
}

async function writePythonEnvironments(root: string, apps: string[]): Promise<void> {
  for (const app of apps) {
    if (await exists(path.join(root, app, PYTHON_PROJECT_FILE))) {
      await run("uv", ["sync", "--quiet"], path.join(root, app));
    }
  }
}

export async function writeTree(root: string, name: string, apps: string[]): Promise<string[]> {
  await rm(root, { recursive: true, force: true });
  for (const app of apps) {
    await copyTree(path.join(repoRoot, app), path.join(root, app));
  }
  const nested = await nestedMembers(repoRoot, apps);
  for (const member of nested) {
    await linkVendored(path.join(repoRoot, member), path.join(root, member));
  }
  const packages = await workspaceClosure(repoRoot, [...apps, ...nested, "."]);
  for (const packageDir of packages) {
    await copyInto(
      path.join(repoRoot, packageDir),
      path.join(root, packageDir),
      NEVER_COPIED_FROM_A_PACKAGE,
    );
  }
  await writeWorkspace(root, name, [...apps, ...nested, ...packages]);
  await writeLockfile(root);
  await writeGoWorkspace(root, apps);
  await linkSdks(root, apps);
  await writePythonEnvironments(root, apps);
  return packages;
}
