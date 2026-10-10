import { createHash } from "node:crypto";
import { SENSITIVE_TOKEN } from "$app/env/private";

export function load() {
  return { sensitiveDigest: createHash("sha256").update(SENSITIVE_TOKEN).digest("hex") };
}
