import { SubstrateError } from "./errors.mjs";

const MAX_APP_LEN = 63;

export function sanitizeAppName(name: string): string {
  let out = "";
  for (const char of name) {
    if (/[a-z0-9]/.test(char)) out += char;
    else if (/[A-Z]/.test(char)) out += char.toLowerCase();
    else if (out.length > 0 && !out.endsWith("-")) out += "-";
  }
  const clamped = trimHyphens(trimHyphens(out).slice(0, MAX_APP_LEN));
  return clamped === "" ? "ocel-worker" : clamped;
}

function trimHyphens(value: string): string {
  return value.replace(/^-+/, "").replace(/-+$/, "");
}

const SEGMENT = /^[A-Za-z0-9._-]+$/;

function segment(name: string, value: unknown): string {
  if (typeof value !== "string" || !SEGMENT.test(value) || value.includes("..")) {
    throw new SubstrateError(`invalid ${name} in request`, value);
  }
  return value;
}

export interface BuildIdentity {
  slug: string;
  app: string;
  buildId: string;
}

export function identity(payload: BuildIdentity): BuildIdentity {
  return {
    slug: segment("slug", payload.slug),
    app: segment("app", sanitizeAppName(String(payload.app ?? ""))),
    buildId: segment("buildId", payload.buildId),
  };
}

export function imageConfigKey(id: BuildIdentity): string {
  return `image-config/${id.slug}/${id.app}/${id.buildId}.json`;
}

export function assetKey(id: BuildIdentity, pathname: string): string {
  return `assets/${id.slug}/${id.app}/${id.buildId}${assetPath(pathname)}`;
}

export function assetPath(pathname: string): string {
  let decoded: string;
  try {
    decoded = decodeURIComponent(pathname);
  } catch (error) {
    throw new SubstrateError("undecodable image path", error);
  }
  if (!decoded.startsWith("/")) {
    throw new SubstrateError("image path is not absolute", decoded);
  }
  const parts = decoded.slice(1).split("/");
  for (const part of parts) {
    if (part === "" || part === "." || part === "..") {
      throw new SubstrateError("image path escapes the build prefix", decoded);
    }
  }
  if (/[\0-\x1f\x7f]/.test(decoded)) {
    throw new SubstrateError("image path holds control characters", decoded);
  }
  return decoded;
}
