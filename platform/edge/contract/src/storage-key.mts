export const objectKinds = [
  "fn",
  "bytecode",
  "assets",
  "pages",
  "route-tables",
  "bundles",
  "image-config",
  "fetch-cache",
  "use-cache",
  "tag-clock",
] as const;

export type ObjectKind = (typeof objectKinds)[number];

export interface StorageScope {
  project: string;
  env?: string;
  app?: string;
  release?: string;
}

export interface StorageCoordinate {
  project: string;
  env: string;
  app: string;
  release: string;
}

export interface ParsedObjectKey extends StorageCoordinate {
  kind: ObjectKind;
  rest: string;
}

const SEPARATOR = "/";
const FIELD_SEPARATOR = "--";
const SLUG = /^[a-z0-9]([a-z0-9-]*[a-z0-9])?$/;
const RELEASE = /^r[0-9a-f]{8}$/;
const COORDINATE_FIELDS = 4;

export function storagePrefix(kind: ObjectKind, scope: StorageScope): string {
  let prefix = `${kind}${SEPARATOR}`;
  for (const field of [scope.project, scope.env, scope.app, scope.release]) {
    if (!field) break;
    prefix += `${field}${SEPARATOR}`;
  }
  return prefix;
}

export function objectKey(kind: ObjectKind, coordinate: StorageCoordinate, rest: string): string {
  return storagePrefix(kind, coordinate) + rest.replace(/^\//, "");
}

export function pagesPrefix(coordinate: StorageCoordinate): string {
  return storagePrefix("pages", coordinate).slice(0, -SEPARATOR.length);
}

function isSlug(value: string): boolean {
  return SLUG.test(value) && !value.includes(FIELD_SEPARATOR);
}

function readCoordinate(fields: readonly string[]): StorageCoordinate | undefined {
  const [project, env, app, release] = fields;
  if (fields.length !== COORDINATE_FIELDS) return undefined;
  if (![project, env, app].every((field) => field !== undefined && isSlug(field))) return undefined;
  if (release === undefined || !RELEASE.test(release)) return undefined;
  return { project: project!, env: env!, app: app!, release };
}

export function parseStorageCoordinate(value: string): StorageCoordinate | undefined {
  return readCoordinate(value.split(SEPARATOR));
}

function isObjectKind(value: string): value is ObjectKind {
  return (objectKinds as readonly string[]).includes(value);
}

export function parseObjectKey(key: string): ParsedObjectKey | undefined {
  const segments = key.split(SEPARATOR);
  const [kind, ...fields] = segments;
  if (kind === undefined || !isObjectKind(kind)) return undefined;
  const coordinate = readCoordinate(fields.slice(0, COORDINATE_FIELDS));
  if (coordinate === undefined) return undefined;
  const rest = fields.slice(COORDINATE_FIELDS);
  if (rest.some((segment) => segment === "" || segment === "." || segment === "..")) {
    return undefined;
  }
  return { kind, ...coordinate, rest: rest.join(SEPARATOR) };
}
