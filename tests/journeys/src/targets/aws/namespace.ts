import { createHash } from "node:crypto";
import { HARNESS_PREFIX, projectSlug } from "../../identity";

export const NAMESPACE_ENV = "OCEL_NAMESPACE";

export const DEFAULT_NAMESPACE = "ocel";

export const LONGEST_NAMESPACE = 44;

const DIGEST_CHARS = 6;

function acceptable(name: string): string {
  const lowered = name.toLowerCase().replace(/[^a-z0-9-]/g, "-");
  return lowered
    .replace(/^[^a-z]+/, "")
    .replace(/-+/g, "-")
    .replace(/-+$/, "");
}

function fitted(name: string): string {
  if (name.length <= LONGEST_NAMESPACE) {
    return name;
  }
  const digest = createHash("sha256").update(name).digest("hex").slice(0, DIGEST_CHARS);
  const head = name.slice(0, LONGEST_NAMESPACE - DIGEST_CHARS - 1).replace(/-+$/, "");
  return `${head}-${digest}`;
}

export function namespaceOfSlug(slug: string): string {
  return fitted(acceptable(slug));
}

export function namespaceFor(cell: string, run: string): string {
  return namespaceOfSlug(projectSlug(cell, run));
}

export function namespaceOf(env: NodeJS.ProcessEnv): string {
  return env[NAMESPACE_ENV]?.trim() || DEFAULT_NAMESPACE;
}

export function bootstrapStackOf(namespace: string): string {
  return `${namespace}-bootstrap`;
}

export function strayNamespaces(found: string[], mine: Iterable<string>): string[] {
  const keep = new Set(mine);
  const stray = new Set<string>();
  for (const namespace of found) {
    if (namespace.startsWith(HARNESS_PREFIX) && !keep.has(namespace)) {
      stray.add(namespace);
    }
  }
  return [...stray];
}
