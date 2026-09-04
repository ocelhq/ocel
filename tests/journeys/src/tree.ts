import { spawn } from "node:child_process";
import { access, cp, readdir, readFile, rm, symlink, writeFile } from "node:fs/promises";
import path from "node:path";
import { repoRoot } from "./paths";

const NEVER_COPIED = [".git", ".next", ".ocel", "dist", "node_modules", "output"];
const NEVER_COPIED_FROM_A_PACKAGE = NEVER_COPIED.filter((name) => name !== "dist");

const WORKSPACE_FILE = "pnpm-workspace.yaml";
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
  const nested = [...named.values()].filter((dir) =>
    appDirs.some((app) => under(dir, app)),
  );
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

export function rootManifest(name: string, carried: Manifest): string {
  const packageManager = packageManagerOf(carried);
  const declared = Object.fromEntries(
    DEPENDENCY_FIELDS.flatMap((field) => {
      const ranges = carried[field];
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

export async function writeWorkspace(
  root: string,
  name: string,
  members: string[],
): Promise<void> {
  const carried = splitWorkspaceFile(await readFile(path.join(repoRoot, WORKSPACE_FILE), "utf8"));
  await writeFile(path.join(root, WORKSPACE_FILE), workspaceFileFor(members, carried.settings));
  await writeFile(
    path.join(root, MANIFEST),
    rootManifest(name, (await readManifest(path.join(repoRoot, MANIFEST))) ?? {}),
  );
}

export async function writeLockfile(root: string): Promise<void> {
  await cp(path.join(repoRoot, LOCKFILE), path.join(root, LOCKFILE));
  const args = ["install", "--lockfile-only", "--ignore-scripts", "--prefer-offline"];
  const said = await new Promise<{ code: number | null; output: string }>((resolve, reject) => {
    const child = spawn("pnpm", args, { cwd: root, env: process.env });
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
    throw new Error(`pnpm ${args.join(" ")} in ${root} exited ${said.code}\n${said.output}`);
  }
}

async function linkVendored(source: string, dest: string): Promise<void> {
  const vendored = path.join(source, "node_modules");
  if (await access(vendored).then(() => true, () => false)) {
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

export async function plantWorkspace(
  root: string,
  name: string,
  apps: string[],
): Promise<string[]> {
  await rm(root, { recursive: true, force: true });
  for (const app of apps) {
    await copyTree(path.join(repoRoot, app), path.join(root, app));
  }
  const nested = await nestedMembers(repoRoot, apps);
  for (const member of nested) {
    await linkVendored(path.join(repoRoot, member), path.join(root, member));
  }
  const packages = await workspaceClosure(repoRoot, [...apps, ...nested, "."]);
  for (const held of packages) {
    await copyInto(path.join(repoRoot, held), path.join(root, held), NEVER_COPIED_FROM_A_PACKAGE);
  }
  await writeWorkspace(root, name, [...apps, ...nested, ...packages]);
  await writeLockfile(root);
  return packages;
}
