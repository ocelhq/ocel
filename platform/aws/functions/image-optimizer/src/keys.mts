import { BootstrapError } from "./errors.mjs";

const SEGMENT = /^[A-Za-z0-9._-]+$/;

const ASSETS = "assets";

const IMAGE_CONFIG = "image-config.json";

export function releaseAssetPrefix(value: unknown): string {
  if (typeof value !== "string" || value === "") {
    throw new BootstrapError("request carries no asset prefix", value);
  }
  const segments = value.split("/");
  if (segments.at(-1) !== ASSETS) {
    throw new BootstrapError(`asset prefix does not end in ${ASSETS}`, value);
  }
  for (const part of segments) {
    if (!SEGMENT.test(part) || part.includes("..")) {
      throw new BootstrapError("asset prefix holds an unusable segment", value);
    }
  }
  return value;
}

export function imageConfigKey(assetPrefix: string): string {
  return assetPrefix.slice(0, -ASSETS.length) + IMAGE_CONFIG;
}

export function assetKey(assetPrefix: string, pathname: string): string {
  return `${assetPrefix}${assetPath(pathname)}`;
}

export function assetPath(pathname: string): string {
  let decoded: string;
  try {
    decoded = decodeURIComponent(pathname);
  } catch (error) {
    throw new BootstrapError("undecodable image path", error);
  }
  if (!decoded.startsWith("/")) {
    throw new BootstrapError("image path is not absolute", decoded);
  }
  const parts = decoded.slice(1).split("/");
  for (const part of parts) {
    if (part === "" || part === "." || part === "..") {
      throw new BootstrapError("image path escapes the build prefix", decoded);
    }
  }
  if (/[\0-\x1f\x7f]/.test(decoded)) {
    throw new BootstrapError("image path holds control characters", decoded);
  }
  return decoded;
}
