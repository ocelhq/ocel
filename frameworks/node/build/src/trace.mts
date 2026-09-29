import { existsSync, readFileSync, statSync } from "node:fs";
import { copyFile, mkdir, readFile, rm, writeFile } from "node:fs/promises";
import path from "node:path";
import { nodeFileTrace } from "@vercel/nft";
import { init as lexerInit, parse as parseImports } from "es-module-lexer";
import ts from "typescript";

const TS_EXT = new Set([".ts", ".tsx", ".mts", ".cts"]);

function transpileTs(source: string, extension: string): string {
  return ts.transpileModule(source, {
    fileName: `f${extension}`,
    compilerOptions: {
      target: ts.ScriptTarget.ESNext,
      module: ts.ModuleKind.ESNext,
      isolatedModules: true,
      jsx: extension === ".tsx" ? ts.JsxEmit.React : ts.JsxEmit.None,
    },
  }).outputText;
}

const RESOLVE_EXT = [".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs"];

function toOutExt(relativePath: string): string {
  const extension = path.extname(relativePath);
  const stem = relativePath.slice(0, -extension.length);
  if (extension === ".ts" || extension === ".tsx") return `${stem}.js`;
  if (extension === ".mts") return `${stem}.mjs`;
  if (extension === ".cts") return `${stem}.cjs`;
  return relativePath;
}

export interface PackageRoot {
  root: string;
  name: string;
}

export interface Placement {
  dest: string;
  owner?: PackageRoot;
}

type PackageCache = Map<string, string | null>;

function readPackageName(packageJson: string): string | null {
  let parsed: unknown;
  try {
    parsed = JSON.parse(readFileSync(packageJson, "utf8"));
  } catch (err) {
    if (err instanceof SyntaxError) return null;
    throw err;
  }
  const name = (parsed as { name?: unknown } | null)?.name;
  return typeof name === "string" && name.length > 0 ? name : null;
}

function findPackage(absolutePath: string, cache: PackageCache): PackageRoot | undefined {
  let dir = path.dirname(absolutePath);
  while (true) {
    let name = cache.get(dir);
    if (name === undefined) {
      const packageJson = path.join(dir, "package.json");
      name = existsSync(packageJson) ? readPackageName(packageJson) : null;
      cache.set(dir, name);
    }
    if (name) return { root: dir, name };
    const parent = path.dirname(dir);
    if (parent === dir) return undefined;
    dir = parent;
  }
}

function isUserFile(absolutePath: string, cwd: string): boolean {
  const relativePath = path.relative(cwd, absolutePath);
  return !relativePath.startsWith("..") && !relativePath.split(path.sep).includes("node_modules");
}

export function placeFile(
  absolutePath: string,
  cwd: string,
  cache: PackageCache = new Map(),
): Placement {
  if (isUserFile(absolutePath, cwd)) {
    return { dest: path.relative(cwd, absolutePath) };
  }
  const owner = findPackage(absolutePath, cache);
  if (owner) {
    return {
      dest: path.join("node_modules", owner.name, path.relative(owner.root, absolutePath)),
      owner,
    };
  }
  return {
    dest: path.join("_external", path.relative(path.parse(absolutePath).root, absolutePath)),
  };
}

export class DuplicatePackageError extends Error {}

export function placeTrace(
  files: string[],
  parentsOf: (absolutePath: string) => string[],
  cwd: string,
  cache: PackageCache = new Map(),
): Map<string, string[]> {
  const placed = new Map(files.map((file) => [file, placeFile(file, cwd, cache)] as const));
  const rootsByName = new Map<string, Set<string>>();
  const packageByRoot = new Map<string, PackageRoot>();
  for (const { owner } of placed.values()) {
    if (!owner) continue;
    packageByRoot.set(owner.root, owner);
    rootsByName.set(owner.name, (rootsByName.get(owner.name) ?? new Set()).add(owner.root));
  }

  const importers = new Map<string, Set<string>>();
  for (const [file, { owner }] of placed) {
    if (!owner) continue;
    for (const parent of parentsOf(file)) {
      const importer = placed.get(parent) ?? placeFile(parent, cwd, cache);
      if (importer.owner?.root === owner.root) continue;
      const found = importers.get(owner.root) ?? new Set<string>();
      found.add(importer.owner ? importer.owner.root : "");
      importers.set(owner.root, found);
    }
  }

  const topLevel = new Map<string, string>();
  for (const [name, roots] of rootsByName) {
    const sorted = [...roots].sort();
    const importedByUser = sorted.filter((root) => importers.get(root)?.has(""));
    if (importedByUser.length > 1) {
      throw new DuplicatePackageError(
        `the app imports two copies of ${name}, from ${importedByUser.join(" and ")}: one function artifact serves one ${name} to its own code. Make the app resolve ${name} to one copy`,
      );
    }
    topLevel.set(name, importedByUser[0] ?? (sorted[0] as string));
  }

  const directories = new Map<string, string[]>();
  const placing = new Set<string>();
  const directoriesOf = (root: string): string[] => {
    const known = directories.get(root);
    if (known) return known;
    const { name } = packageByRoot.get(root) as PackageRoot;
    if (topLevel.get(name) === root) {
      const top = [path.join("node_modules", name)];
      directories.set(root, top);
      return top;
    }
    if (placing.has(root)) {
      throw new DuplicatePackageError(
        `${name} at ${root} is a second copy that depends on itself through other second copies, so it has no place to nest in the function artifact`,
      );
    }
    placing.add(root);
    const nested = [...(importers.get(root) ?? [])]
      .filter((importer) => importer !== "")
      .sort()
      .flatMap((importer) =>
        directoriesOf(importer).map((dir) => path.join(dir, "node_modules", name)),
      );
    placing.delete(root);
    if (nested.length === 0) {
      throw new DuplicatePackageError(
        `${name} is traced from ${[...(rootsByName.get(name) ?? [])].sort().join(" and ")}, and no package imports the copy at ${root}, so it has no place beside the copy at ${topLevel.get(name)}`,
      );
    }
    directories.set(root, nested);
    return nested;
  };

  const out = new Map<string, string[]>();
  for (const [file, placement] of placed) {
    const { owner } = placement;
    out.set(
      file,
      owner
        ? directoriesOf(owner.root).map((dir) => path.join(dir, path.relative(owner.root, file)))
        : [placement.dest],
    );
  }
  for (const [root] of packageByRoot) {
    const packageJson = path.join(root, "package.json");
    if (!out.has(packageJson) && existsSync(packageJson)) {
      out.set(
        packageJson,
        directoriesOf(root).map((dir) => path.join(dir, "package.json")),
      );
    }
  }
  return out;
}

function isUserSource(absolutePath: string): boolean {
  return (
    !absolutePath.includes(`${path.sep}node_modules${path.sep}`) &&
    TS_EXT.has(path.extname(absolutePath))
  );
}

function emittedExt(sourceExtension: string): string {
  return path.extname(toOutExt(`f${sourceExtension}`)) || sourceExtension;
}

function rewriteSpecifier(specifier: string, sourceDir: string): string | undefined {
  if (!specifier.startsWith("./") && !specifier.startsWith("../")) return undefined;
  if (/\.(js|mjs|cjs)$/.test(specifier)) return undefined;

  const resolved = path.resolve(sourceDir, specifier);
  for (const extension of RESOLVE_EXT) {
    if (existsSync(resolved + extension)) return specifier + emittedExt(extension);
  }
  if (existsSync(resolved) && statSync(resolved).isDirectory()) {
    for (const extension of RESOLVE_EXT) {
      if (existsSync(path.join(resolved, `index${extension}`))) {
        return `${specifier.replace(/\/$/, "")}/index${emittedExt(extension)}`;
      }
    }
  }
  return undefined;
}

function isLexerParseError(err: unknown): boolean {
  return err instanceof Error && "idx" in err;
}

async function rewriteRelativeImports(code: string, sourceDir: string): Promise<string> {
  await lexerInit;
  let imports: ReturnType<typeof parseImports>[0];
  try {
    [imports] = parseImports(code);
  } catch (err) {
    if (isLexerParseError(err)) return code;
    throw err;
  }
  let out = code;
  for (let i = imports.length - 1; i >= 0; i--) {
    const found = imports[i]!;
    const specifier = found.n;
    if (!specifier || out.slice(found.s, found.e) !== specifier) continue;
    const rewritten = rewriteSpecifier(specifier, sourceDir);
    if (rewritten && rewritten !== specifier) {
      out = out.slice(0, found.s) + rewritten + out.slice(found.e);
    }
  }
  return out;
}

function traceBase(cwd: string): string {
  let base = cwd;
  let dir = cwd;
  while (true) {
    if (existsSync(path.join(dir, "node_modules"))) base = dir;
    const parent = path.dirname(dir);
    if (parent === dir) break;
    dir = parent;
  }
  return base;
}

async function traceReadFile(file: string): Promise<Buffer | string | null> {
  let buf: Buffer;
  try {
    buf = await readFile(file);
  } catch (err) {
    const code = (err as NodeJS.ErrnoException).code;
    if (code === "ENOENT" || code === "EISDIR" || code === "ENOTDIR") return null;
    throw err;
  }
  const extension = path.extname(file);
  if (TS_EXT.has(extension)) return transpileTs(buf.toString("utf8"), extension);
  return buf;
}

async function emitFile(absolutePath: string, dest: string): Promise<void> {
  if (statSync(absolutePath).isDirectory()) return;
  await mkdir(path.dirname(dest), { recursive: true });
  if (isUserSource(absolutePath)) {
    const source = await readFile(absolutePath, "utf8");
    const code = transpileTs(source, path.extname(absolutePath));
    const rewritten = await rewriteRelativeImports(code, path.dirname(absolutePath));
    await writeFile(toOutExt(dest), rewritten);
    return;
  }
  const extension = path.extname(absolutePath);
  if (extension === ".js" || extension === ".mjs") {
    const source = await readFile(absolutePath, "utf8");
    const rewritten = await rewriteRelativeImports(source, path.dirname(absolutePath));
    if (rewritten !== source) {
      await writeFile(dest, rewritten);
      return;
    }
  }
  await copyFile(absolutePath, dest);
}

export interface TraceRequest {
  cwd: string;
  entrypoint: string;
  functionDir: string;
}

export async function traceFunction({ cwd, entrypoint, functionDir }: TraceRequest): Promise<void> {
  await rm(functionDir, { recursive: true, force: true });
  await mkdir(functionDir, { recursive: true });

  const base = traceBase(cwd);
  const { fileList, reasons } = await nodeFileTrace([entrypoint], {
    base,
    readFile: traceReadFile,
  });
  const absoluteOf = (relativePath: string) => path.resolve(base, relativePath);
  const placements = placeTrace(
    [...fileList].map(absoluteOf),
    (absolutePath) =>
      [...(reasons.get(path.relative(base, absolutePath))?.parents ?? [])].map(absoluteOf),
    cwd,
  );
  for (const [absolutePath, dests] of placements) {
    for (const dest of dests) {
      await emitFile(absolutePath, path.join(functionDir, dest));
    }
  }
}
