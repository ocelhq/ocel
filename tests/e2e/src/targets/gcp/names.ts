import { createHash } from "node:crypto";
import { sanitize } from "../../naming";

export const NAMESPACE_ENV = "OCEL_NAMESPACE";

export const DEFAULT_NAMESPACE = "ocel";

export const NAMESPACE_LABEL = "ocel-namespace";

export const PROJECT_LABEL = "ocel-project";

export const APP_LABEL = "ocel-app";

export const TIER_LABEL = "ocel-tier";

export const PRODUCTION_TIER = "production";

export const ENVIRONMENT_LABEL = "ocel-environment";

export const PRODUCTION_ENVIRONMENT = "prod";

const LONGEST_LABEL = 63;
const TAG_CHARS = 6;
const LONGEST_NAMESPACE_FIELD = 12;
const NAMESPACE_HEAD_CHARS = LONGEST_NAMESPACE_FIELD - 1 - TAG_CHARS;
const APP_WORD = "app";

function hashTag(value: string): string {
  return createHash("sha256").update(value).digest("hex").slice(0, TAG_CHARS);
}

export function labelValue(value: string): string {
  const sanitized = sanitize(value);
  if (sanitized.length <= LONGEST_LABEL) {
    return sanitized;
  }
  const head = sanitized.slice(0, LONGEST_LABEL - TAG_CHARS - 2).replace(/-+$/, "");
  return `${head}-${hashTag(sanitized)}`;
}

export function namespaceField(namespace: string): string {
  if (namespace.length <= LONGEST_NAMESPACE_FIELD) {
    return namespace;
  }
  let head = namespace.slice(0, NAMESPACE_HEAD_CHARS).replace(/-+$/, "");
  if (`-${head}`.endsWith(`-${APP_WORD}`)) {
    head = head.slice(0, -1).replace(/-+$/, "");
  }
  return `${head}-${hashTag(namespace)}`;
}

export function namespaceOf(env: NodeJS.ProcessEnv): string {
  return env[NAMESPACE_ENV]?.trim() || DEFAULT_NAMESPACE;
}
