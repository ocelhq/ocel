import { createHash } from "node:crypto";
import { sanitize } from "../../naming";
import type { CellUnderTest } from "../../run/cellRun";

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
  cell: Pick<CellUnderTest, "slug" | "fixture">,
  env: NodeJS.ProcessEnv,
): string {
  return fittedSlug(cell.slug, roomForSlug(namespaceOf(env), cell.fixture.apps));
}

const LONGEST_APP_SERVICE = 37;
const FUNCTION_KIND = "fn";
const FIELD_SEPARATOR = "--";

export function serviceNames(namespace: string, slug: string, app: string): string[] {
  const routed = [FUNCTION_KIND, app, ONLY_ROUTE].join(FIELD_SEPARATOR);
  return [serviceName(namespace, slug, app, app), serviceName(namespace, slug, app, routed)];
}

function serviceName(namespace: string, slug: string, app: string, fn: string): string {
  const parts = [namespace, sanitize(slug), PRODUCTION, sanitize(app)];
  if (fn !== app) {
    parts.push(
      sanitize(fn.slice(`${FUNCTION_KIND}${FIELD_SEPARATOR}${app}${FIELD_SEPARATOR}`.length)),
    );
  }
  const hash = createHash("sha256")
    .update([namespace, slug, PRODUCTION, app, fn].join("\0"))
    .digest("hex")
    .slice(0, DIGEST_CHARS);
  return `${fitReadable(parts, LONGEST_APP_SERVICE - DIGEST_CHARS - 1)}-${hash}`;
}

function fitReadable(parts: string[], room: number): string {
  for (const cut of [1, 2]) {
    const over = parts.join("-").length - room;
    if (over <= 0) {
      break;
    }
    const part = parts[cut] ?? "";
    parts[cut] = part.slice(0, Math.max(part.length - over, 1)).replace(/^-+|-+$/g, "");
  }
  return parts.join("-").slice(0, room).replace(/-+$/, "");
}
