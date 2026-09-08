import { createHash } from "node:crypto";
import type { CellContext } from "../types";

export const NAMESPACE_ENV = "OCEL_NAMESPACE";

export const DEFAULT_NAMESPACE = "ocel";

const LONGEST_SERVICE = 49;
const DIGEST_CHARS = 6;
const PRODUCTION = "prod";
const ONLY_ROUTE = "index";
const SEPARATORS = 5;

export function namespaceOf(env: NodeJS.ProcessEnv): string {
  return env[NAMESPACE_ENV]?.trim() || DEFAULT_NAMESPACE;
}

export function sanitize(value: string): string {
  return value
    .toLowerCase()
    .replace(/[^a-z0-9]/g, "-")
    .replace(/-+/g, "-")
    .replace(/^-|-$/g, "");
}

export function roomForSlug(namespace: string, apps: string[]): number {
  const longest = Math.max(...apps.map((app) => sanitize(app).length));
  return (
    LONGEST_SERVICE -
    namespace.length -
    PRODUCTION.length -
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
  cell: Pick<CellContext, "slug" | "fixture">,
  env: NodeJS.ProcessEnv,
): string {
  return fittedSlug(cell.slug, roomForSlug(namespaceOf(env), cell.fixture.apps));
}

export function serviceLead(namespace: string, slug: string, app: string): string {
  return [namespace, sanitize(slug), PRODUCTION, sanitize(app)].join("-");
}
