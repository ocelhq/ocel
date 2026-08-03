import { SubstrateError } from "./errors.mjs";

// Deriving the S3 keys this function reads, from three identifiers and a path
// that all arrived over the wire.
//
// The bucket holds every app in the account, so a key this function assembles
// wrong is a key into another tenant's build. Nothing here is normalized into
// safety — a component that is not already exactly what the deploy wrote is
// refused, because "normalize it until it looks fine" is the shape every
// traversal bypass has taken.

// Matches cloud/aws/deploy/edgeworker.go's sanitizeWorkerName, which is what
// the uploader applied to the app name before writing these keys. The worker's
// OCEL_APP carries the raw name, so this function is where the two are made to
// agree; deriving the key from the raw name would simply miss the object.
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

// A slug or build id is a single path segment and nothing else. Anything with a
// separator, a dot run, or a character no deploy would have written is refused
// rather than escaped.
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

// Deliberately outside the assets/ prefix: that prefix is the app's public web
// root — the worker serves any unmatched request path out of it — so a config
// under it would be served to the internet with the allowed remote hostnames
// and the dangerouslyAllowSVG setting in it, and would collide exactly with a
// project's own public/image-config.json. Must stay identical to
// imageConfigKey in cloud/aws/deploy/assets.go.
export function imageConfigKey(id: BuildIdentity): string {
  return `image-config/${id.slug}/${id.app}/${id.buildId}.json`;
}

// The same prefix cloud/aws/deploy/assets.go publishes static assets under, and
// the same one the worker joins a request pathname onto.
export function assetKey(id: BuildIdentity, pathname: string): string {
  return `assets/${id.slug}/${id.app}/${id.buildId}${assetPath(pathname)}`;
}

// The path of a local image, as the deploy keyed it: decoded, since the deploy
// wrote real file names and a browser sends them percent-encoded, and then
// checked rather than re-normalized.
//
// Decoding is what makes the check necessary and is also why it cannot be
// skipped: new URL() already collapsed a literal ../, but %2e%2e%2f only
// becomes one here, after every parser that would have collapsed it has run.
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
  // A NUL truncates the key for anything downstream written in C, and no
  // deployed file name contains one.
  if (/[\0-\x1f\x7f]/.test(decoded)) {
    throw new SubstrateError("image path holds control characters", decoded);
  }
  return decoded;
}
