import { existsSync, readFileSync } from "node:fs";
import path from "node:path";

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

export function sanitizeName(raw: string): string {
  return raw.replace(/[^A-Za-z0-9_-]+/g, "-").replace(/^-+|-+$/g, "");
}
