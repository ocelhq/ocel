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
export const fieldSeparator = "--";
const SLUG = /^[a-z0-9]([a-z0-9-]*[a-z0-9])?$/;
const RELEASE = /^r[0-9a-f]{8}$/;
const COORDINATE_FIELDS = 4;

function isSlug(value: string): boolean {
  return SLUG.test(value) && !value.includes(fieldSeparator);
}

export function isStorageCoordinate(value: unknown): value is StorageCoordinate {
  if (typeof value !== "object" || value === null) return false;
  const { project, env, app, release } = value as Record<string, unknown>;
  return (
    [project, env, app].every((field) => typeof field === "string" && isSlug(field)) &&
    typeof release === "string" &&
    RELEASE.test(release)
  );
}

export function formatStorageCoordinate({ project, env, app, release }: StorageCoordinate): string {
  return [project, env, app, release].join(SEPARATOR);
}

export function storagePrefix(kind: ObjectKind, coordinate: StorageCoordinate): string {
  return `${kind}${SEPARATOR}${formatStorageCoordinate(coordinate)}${SEPARATOR}`;
}

export function objectKey(kind: ObjectKind, coordinate: StorageCoordinate, rest: string): string {
  return storagePrefix(kind, coordinate) + rest.replace(/^\//, "");
}

function readCoordinate(fields: readonly string[]): StorageCoordinate | undefined {
  if (fields.length !== COORDINATE_FIELDS) return undefined;
  const [project, env, app, release] = fields as [string, string, string, string];
  const coordinate = { project, env, app, release };
  return isStorageCoordinate(coordinate) ? coordinate : undefined;
}

export function parseStorageCoordinate(value: string): StorageCoordinate | undefined {
  return readCoordinate(value.split(SEPARATOR));
}

function isObjectKind(value: string): value is ObjectKind {
  return (objectKinds as readonly string[]).includes(value);
}

export function parseObjectKey(key: string): ParsedObjectKey | undefined {
  const [kind, ...fields] = key.split(SEPARATOR);
  if (kind === undefined || !isObjectKind(kind)) return undefined;
  const coordinate = readCoordinate(fields.slice(0, COORDINATE_FIELDS));
  if (coordinate === undefined) return undefined;
  const rest = fields.slice(COORDINATE_FIELDS);
  if (
    rest.length === 0 ||
    rest.some((segment) => segment === "" || segment === "." || segment === "..")
  ) {
    return undefined;
  }
  return { kind, ...coordinate, rest: rest.join(SEPARATOR) };
}
