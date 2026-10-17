import { existsSync, readFileSync, realpathSync, statSync } from "node:fs";
import { builtinModules } from "node:module";
import { dirname, join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import ts from "typescript";
import { describe, expect, it } from "vitest";

const here = dirname(fileURLToPath(import.meta.url));
const src = resolve(here, "..");
const packageRoot = resolve(src, "..");

const edgeConditions = new Set(["edge-light", "workerd", "worker", "import", "default"]);

function isFile(path: string): boolean {
  return existsSync(path) && statSync(path).isFile();
}

function readManifest(dir: string): Record<string, unknown> {
  return JSON.parse(readFileSync(join(dir, "package.json"), "utf8"));
}

function edgeTarget(target: unknown): string | undefined {
  if (typeof target === "string") return target;
  if (!target || typeof target !== "object" || Array.isArray(target)) return undefined;
  for (const [condition, value] of Object.entries(target)) {
    if (!edgeConditions.has(condition)) continue;
    const resolved = edgeTarget(value);
    if (resolved) return resolved;
  }
  return undefined;
}

function relativeModule(specifier: string, from: string): string | undefined {
  const base = resolve(dirname(from), specifier);
  if (from.startsWith(src) && !from.includes("node_modules")) {
    const source = base.replace(/\.js$/, "");
    return [`${source}.ts`, join(source, "index.ts")].find(isFile);
  }
  return [base, `${base}.js`, join(base, "index.js")].find(isFile);
}

function sourceOf(distTarget: string): string | undefined {
  const source = resolve(packageRoot, distTarget.replace(/^\.\/dist\//, "./src/")).replace(
    /\.js$/,
    ".ts",
  );
  return isFile(source) ? source : undefined;
}

function packageImport(specifier: string): string | undefined {
  const imports = readManifest(packageRoot).imports as Record<string, unknown> | undefined;
  const target = edgeTarget(imports?.[specifier]);
  return target ? sourceOf(target) : undefined;
}

function packageDir(name: string, from: string): string | undefined {
  let dir = dirname(realpathSync(from));
  for (;;) {
    const candidate = join(dir, "node_modules", name);
    if (isFile(join(candidate, "package.json"))) return realpathSync(candidate);
    const parent = dirname(dir);
    if (parent === dir) return undefined;
    dir = parent;
  }
}

function thirdParty(specifier: string, from: string): string | undefined {
  const segments = specifier.split("/");
  const name = segments.slice(0, specifier.startsWith("@") ? 2 : 1).join("/");
  const dir = packageDir(name, from);
  if (!dir) return undefined;

  const manifest = readManifest(dir);
  const subpath = `.${specifier.slice(name.length)}`;
  const exports = manifest.exports;
  let target: string | undefined;
  if (exports === undefined) {
    target =
      subpath === "." ? ((manifest.module ?? manifest.main ?? "index.js") as string) : subpath;
  } else {
    const map =
      typeof exports === "object" &&
      exports !== null &&
      Object.keys(exports).some((key) => key.startsWith("."))
        ? (exports as Record<string, unknown>)
        : { ".": exports };
    target = edgeTarget(map[subpath]);
  }
  if (!target) return undefined;
  const entry = resolve(dir, target);
  return [entry, `${entry}.js`, join(entry, "index.js")].find(isFile);
}

function resolveModule(specifier: string, from: string): string | undefined {
  if (specifier.startsWith(".")) return relativeModule(specifier, from);
  if (specifier.startsWith("#")) return packageImport(specifier);
  return thirdParty(specifier, from);
}

function isBuiltin(specifier: string): boolean {
  return specifier.startsWith("node:") || builtinModules.includes(specifier);
}

function displayPath(file: string): string {
  const marker = file.lastIndexOf("node_modules/");
  return marker === -1 ? relative(src, file) : file.slice(marker + "node_modules/".length);
}

function typeOnly(clause: ts.ImportClause | undefined): boolean {
  if (!clause) return false;
  if (clause.isTypeOnly) return true;
  if (clause.name) return false;
  const bindings = clause.namedBindings;
  if (!bindings || !ts.isNamedImports(bindings)) return false;
  return bindings.elements.every((element) => element.isTypeOnly);
}

function loaded(file: string): string[] {
  const source = ts.createSourceFile(
    file,
    readFileSync(file, "utf8"),
    ts.ScriptTarget.ESNext,
    true,
  );
  const specifiers: string[] = [];

  const visit = (node: ts.Node): void => {
    if (ts.isImportDeclaration(node) && ts.isStringLiteral(node.moduleSpecifier)) {
      if (!typeOnly(node.importClause)) specifiers.push(node.moduleSpecifier.text);
    } else if (
      ts.isExportDeclaration(node) &&
      !node.isTypeOnly &&
      node.moduleSpecifier &&
      ts.isStringLiteral(node.moduleSpecifier)
    ) {
      specifiers.push(node.moduleSpecifier.text);
    } else if (
      ts.isCallExpression(node) &&
      node.expression.kind === ts.SyntaxKind.ImportKeyword &&
      node.arguments[0] &&
      ts.isStringLiteral(node.arguments[0])
    ) {
      specifiers.push(node.arguments[0].text);
    }
    ts.forEachChild(node, visit);
  };

  visit(source);
  return specifiers;
}

function nodeBuiltins(entry: string): string[] {
  const seen = new Set<string>();
  const found: string[] = [];
  const queue = [entry];

  while (queue.length > 0) {
    const file = queue.pop()!;
    if (seen.has(file)) continue;
    seen.add(file);

    for (const specifier of loaded(file)) {
      if (isBuiltin(specifier)) {
        found.push(`${displayPath(file)} loads ${specifier}`);
        continue;
      }
      const next = resolveModule(specifier, file);
      if (!next) {
        found.push(
          `${displayPath(file)} loads ${specifier}, which does not resolve for an edge bundle`,
        );
        continue;
      }
      queue.push(next);
    }
  }

  return found.sort();
}

describe("the bucket entry next serves", () => {
  it("loads no node builtin, of its own or through a package it imports, so an edge bundle can include its modules", () => {
    expect(nodeBuiltins(join(here, "next.ts"))).toEqual([]);
  });
});
