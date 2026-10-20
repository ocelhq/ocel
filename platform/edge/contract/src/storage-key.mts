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

export const storageCoordinateVar = "OCEL_STORAGE_COORDINATE";

export const bucketVars = {
  artifacts: "OCEL_ARTIFACTS_BUCKET",
  edge: "OCEL_EDGE_BUCKET",
  cache: "OCEL_CACHE_BUCKET",
} as const;

const SEPARATOR = "/";
const FIELD_SEPARATOR = "--";
const SLUG = /^[a-z0-9]([a-z0-9-]*[a-z0-9])?$/;
const RELEASE = /^r[0-9a-f]{8}$/;
const COORDINATE_FIELDS = 4;

function isSlug(value: string): boolean {
  return SLUG.test(value) && !value.includes(FIELD_SEPARATOR);
}

export function isStorageCoordinate({ project, env, app, release }: StorageCoordinate): boolean {
  return [project, env, app].every(isSlug) && RELEASE.test(release);
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

export function assetPrefix(coordinate: StorageCoordinate): string {
  return `assets${SEPARATOR}${formatStorageCoordinate(coordinate)}`;
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

export function parseAssetPrefix(value: string): StorageCoordinate | undefined {
  const [kind, ...fields] = value.split(SEPARATOR);
  return kind === "assets" ? readCoordinate(fields) : undefined;
}

export function readStorageCoordinate(
  env: Readonly<Record<string, string | undefined>>,
): StorageCoordinate | undefined {
  const value = env[storageCoordinateVar];
  if (!value) return undefined;
  const coordinate = parseStorageCoordinate(value);
  if (coordinate === undefined) {
    throw new Error(
      `ocel: ${storageCoordinateVar} ${JSON.stringify(value)} is not <project>/<env>/<app>/<release token>`,
    );
  }
  return coordinate;
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
