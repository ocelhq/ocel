import { createHash } from "node:crypto";

const LONGEST_NAMESPACE_FIELD = 12;

const NAMESPACE_FIELD_DIGEST_CHARS = 6;

const NAMESPACE_HEAD_CHARS = LONGEST_NAMESPACE_FIELD - 1 - NAMESPACE_FIELD_DIGEST_CHARS;

const APP_WORD = "app";

export function namespaceField(namespace: string): string {
  if (namespace.length <= LONGEST_NAMESPACE_FIELD) {
    return namespace;
  }
  let head = namespace.slice(0, NAMESPACE_HEAD_CHARS).replace(/-+$/, "");
  if (`-${head}`.endsWith(`-${APP_WORD}`)) {
    head = head.slice(0, -1).replace(/-+$/, "");
  }
  const digest = createHash("sha256")
    .update(namespace)
    .digest("hex")
    .slice(0, NAMESPACE_FIELD_DIGEST_CHARS);
  return `${head}-${digest}`;
}
