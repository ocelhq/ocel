import type { FileInfo, PathConfig } from "./types.js";

function randomToken(): string {
  return crypto.randomUUID().replace(/-/g, "").slice(0, 8);
}

function sanitize(name: string): string {
  const base = name.split(/[\\/]/).pop() ?? name;
  return base.replace(/[^a-zA-Z0-9._-]/g, "-").replace(/^\.+/, "");
}

function withSuffix(name: string, token: string): string {
  const dot = name.lastIndexOf(".");
  if (dot <= 0) return `${name}-${token}`;
  return `${name.slice(0, dot)}-${token}${name.slice(dot)}`;
}

/**
 * Computes the object key for one file from the uploader's path config. The
 * structured form yields `prefix + sanitized(name)`, inserting a random token
 * before the extension when randomSuffix is set. The function form hands full
 * control to the user.
 */
export function generateKey(
  path: PathConfig<unknown> | undefined,
  ctx: { file: FileInfo; metadata: unknown },
): string {
  if (typeof path === "function") {
    return path(ctx);
  }

  const prefix = path?.prefix ?? "";
  let name = sanitize(ctx.file.name);
  if (path?.randomSuffix) {
    name = withSuffix(name, randomToken());
  }
  return `${prefix}${name}`;
}
