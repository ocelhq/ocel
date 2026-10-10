import { mkdirSync, rmSync, writeFileSync } from "node:fs";
import { copyFile, cp, lstat, mkdir, readlink, rm, stat, symlink } from "node:fs/promises";
import path from "node:path";
import type { Hosting } from "@platform/edge-contract/hosting";
import { Refusal } from "./refusal.mjs";

export const OUTPUT_DIR_ENV = "OCEL_OUTPUT_DIR";
export const APP_NAME_ENV = "OCEL_APP_NAME";
export const APP_FOLDER_ENV = "OCEL_APP_FOLDER";

export const HOSTING_FILE = "hosting.json";
export const HOSTING_VERSION = 1;
export const FUNCTION_CONFIG_FILE = "function-config.json";
export const FUNCTIONS_DIR = "functions";
export const FUNCTION_DIR_SUFFIX = ".func";
export const STATIC_DIR = "static";
export const ROOT_FUNCTION = "/";
export const ROOT_FUNCTION_DIR = `index${FUNCTION_DIR_SUFFIX}`;

export interface FunctionConfig {
  framework: { name: string };
  entryFile: string;
  id: string;
  app: string;
}

export class BuildOutput {
  readonly dir: string;
  readonly app: string;

  constructor(dir: string, app: string) {
    this.dir = dir;
    this.app = app;
  }

  static fromEnv(defaults: { dir: string; app: string }): BuildOutput {
    return new BuildOutput(
      path.resolve(process.env[OUTPUT_DIR_ENV] || defaults.dir),
      process.env[APP_NAME_ENV] || defaults.app,
    );
  }

  get staticDir(): string {
    return path.join(this.dir, STATIC_DIR);
  }

  functionDir(id: string): string {
    const name = id === ROOT_FUNCTION ? ROOT_FUNCTION_DIR : `${id}${FUNCTION_DIR_SUFFIX}`;
    return path.join(this.dir, FUNCTIONS_DIR, name);
  }

  clean(): void {
    for (const owned of [FUNCTIONS_DIR, STATIC_DIR, HOSTING_FILE]) {
      rmSync(path.join(this.dir, owned), { force: true, recursive: true });
    }
  }

  writeFile(file: string, content: string): void {
    const dest = path.join(this.dir, file);
    mkdirSync(path.dirname(dest), { recursive: true });
    writeFileSync(dest, content);
  }

  writeHosting(hosting: Omit<Hosting, "version">): void {
    this.writeFile(HOSTING_FILE, JSON.stringify({ version: HOSTING_VERSION, ...hosting }));
  }

  writeFunctionConfig(id: string, framework: string, entryFile: string): void {
    const config: FunctionConfig = { framework: { name: framework }, entryFile, id, app: this.app };
    writeFileSync(path.join(this.functionDir(id), FUNCTION_CONFIG_FILE), JSON.stringify(config));
  }

  async copyIntoFunction(id: string, assets: Record<string, string>, root: string): Promise<void> {
    const functionDir = this.functionDir(id);
    const mirrored = mirroredPaths(Object.keys(assets));
    for (const [dest, src] of Object.entries(assets)) {
      const placed = path.join(functionDir, dest);
      if (containedIn(functionDir, placed) === undefined) {
        throw new Refusal(
          `ocel: the traced asset ${src} would land at ${dest}, outside the function ${id}, so the function cannot carry it`,
        );
      }
      await copyAsset(src, placed, root, functionDir, mirrored);
    }
  }
}

export function toSlash(file: string): string {
  return file.split(path.sep).join("/");
}

function containedIn(root: string, target: string): string | undefined {
  const rel = path.relative(root, target);
  if (path.isAbsolute(rel) || rel === ".." || rel.startsWith(`..${path.sep}`)) {
    return undefined;
  }
  return rel;
}

function mirroredPaths(destKeys: readonly string[]): ReadonlySet<string> {
  const paths = new Set<string>();
  for (const key of destKeys) {
    const parts = key.split("/");
    for (let i = 1; i <= parts.length; i++) {
      paths.add(parts.slice(0, i).join("/"));
    }
  }
  return paths;
}

function nothingIfMissing(error: NodeJS.ErrnoException): undefined {
  if (error.code === "ENOENT") return undefined;
  throw error;
}

async function copyAsset(
  srcAbs: string,
  dest: string,
  root: string,
  functionDir: string,
  mirrored: ReadonlySet<string>,
): Promise<void> {
  const info = await lstat(srcAbs).catch(nothingIfMissing);
  if (!info) return;
  await mkdir(path.dirname(dest), { recursive: true });
  if (info.isSymbolicLink()) {
    const raw = await readlink(srcAbs);
    const target = path.isAbsolute(raw) ? raw : path.resolve(path.dirname(srcAbs), raw);
    const contained = containedIn(root, target);
    const rel = contained !== undefined && mirrored.has(toSlash(contained)) ? contained : undefined;
    await rm(dest, { recursive: true, force: true });
    if (rel !== undefined) {
      await symlink(toSlash(path.relative(path.dirname(dest), path.join(functionDir, rel))), dest);
      return;
    }
    const targetInfo = await stat(target).catch(nothingIfMissing);
    if (!targetInfo) return;
    if (targetInfo.isDirectory()) {
      await cp(target, dest, { recursive: true, dereference: true });
      return;
    }
    await copyFile(target, dest);
    return;
  }
  if (info.isDirectory()) {
    await cp(srcAbs, dest, { recursive: true });
    return;
  }
  await copyFile(srcAbs, dest);
}
