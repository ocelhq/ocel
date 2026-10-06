import { createHash } from "node:crypto";
import { sanitize } from "../../naming";
import type { CellUnderTest } from "../../run/cellRun";

export const NAMESPACE_ENV = "OCEL_NAMESPACE";

export const DEFAULT_NAMESPACE = "ocel";

export const NAMESPACE_LABEL = "ocel-namespace";

export const PROJECT_LABEL = "ocel-project";

export const APP_LABEL = "ocel-app";

export const ENVIRONMENT_LABEL = "ocel-environment";

export const PRODUCTION_ENVIRONMENT = "prod";

const LONGEST_LABEL = 63;
const LABEL_DIGEST_CHARS = 8;
const TRUNCATION_MARKER = "-x";

export function labelValue(value: string): string {
  const sanitized = sanitize(value);
  if (sanitized.length <= LONGEST_LABEL) {
    return sanitized;
  }
  const digest = createHash("sha256").update(sanitized).digest("hex").slice(0, LABEL_DIGEST_CHARS);
  const head = sanitized
    .slice(0, LONGEST_LABEL - LABEL_DIGEST_CHARS - TRUNCATION_MARKER.length)
    .replace(/-+$/, "");
  return `${head}${TRUNCATION_MARKER}${digest}`;
}

const LONGEST_SERVICE = 49;
const DIGEST_CHARS = 6;
const ONLY_ROUTE = "index";
const SEPARATORS = 5;

export function namespaceOf(env: NodeJS.ProcessEnv): string {
  return env[NAMESPACE_ENV]?.trim() || DEFAULT_NAMESPACE;
}

export function roomForSlug(namespace: string, apps: string[]): number {
  const longest = Math.max(...apps.map((app) => sanitize(app).length));
  return (
    LONGEST_SERVICE -
    namespace.length -
    PRODUCTION_ENVIRONMENT.length -
    longest -
    ONLY_ROUTE.length -
    DIGEST_CHARS -
    SEPARATORS
  );
}

export function fittedSlug(slug: string, room: number): string {
  if (room < DIGEST_CHARS + 4) {
    throw new Error(
      `a Cloud Run service name leaves ${room} characters of room for ${slug}, and a slug shorter ` +
        `than ${DIGEST_CHARS + 4} tells no two cells apart. Name a shorter namespace in ${NAMESPACE_ENV}`,
    );
  }
  if (slug.length <= room) {
    return slug;
  }
  const digest = createHash("sha256").update(slug).digest("hex").slice(0, DIGEST_CHARS);
  const head = slug.slice(0, room - DIGEST_CHARS - 1).replace(/-+$/, "");
  return `${head}-${digest}`;
}

export function gcpSlug(
  cell: Pick<CellUnderTest, "slug" | "fixture">,
  env: NodeJS.ProcessEnv,
): string {
  return fittedSlug(cell.slug, roomForSlug(namespaceOf(env), cell.fixture.apps));
}
