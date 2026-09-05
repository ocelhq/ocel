import { existsSync, readFileSync } from "node:fs";
import path from "node:path";

export function hasPackageJson(dir: string): boolean {
  return existsSync(path.join(dir, "package.json"));
}

export function hasDep(dir: string, name: string): boolean {
  const pj = path.join(dir, "package.json");
  if (!existsSync(pj)) return false;
  try {
    const pkg = JSON.parse(readFileSync(pj, "utf8"));
    return Boolean(pkg.dependencies?.[name] ?? pkg.devDependencies?.[name]);
  } catch {
    return false;
  }
}
