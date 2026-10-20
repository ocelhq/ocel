import { objectKey, parseObjectKey } from "@platform/edge-contract/storage-key";
import { BootstrapError } from "./errors.mjs";

const IMAGE_CONFIG = "image-config.json";

export function releaseAssetPrefix(value: unknown): string {
  if (typeof value !== "string" || value === "") {
    throw new BootstrapError("request has no asset prefix", value);
  }
  const key = parseObjectKey(value);
  if (key?.kind !== "assets" || key.rest !== "") {
    throw new BootstrapError("asset prefix names no release's assets", value);
  }
  return value;
}

export function imageConfigKey(assetPrefix: string): string {
  const key = parseObjectKey(assetPrefix);
  if (key === undefined) throw new BootstrapError("asset prefix names no release", assetPrefix);
  return objectKey("image-config", key, IMAGE_CONFIG);
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
  if (hasControlCharacter(decoded)) {
    throw new BootstrapError("image path contains control characters", decoded);
  }
  return decoded;
}

function hasControlCharacter(value: string): boolean {
  for (const ch of value) {
    const code = ch.codePointAt(0) ?? 0;
    if (code <= 0x1f || code === 0x7f) return true;
  }
  return false;
}
