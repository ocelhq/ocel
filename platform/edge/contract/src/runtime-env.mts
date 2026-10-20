import { parseStorageCoordinate, type StorageCoordinate } from "./storage-key.mjs";

export const storageCoordinateVar = "OCEL_STORAGE_COORDINATE";

export const bucketVars = {
  artifacts: "OCEL_ARTIFACTS_BUCKET",
  edge: "OCEL_EDGE_BUCKET",
  cache: "OCEL_CACHE_BUCKET",
} as const;

export const isrVar = "OCEL_ISR";

export const routerKindVar = "OCEL_ROUTER_KIND";

export const routeTableVar = "OCEL_NEXT_ROUTE_TABLE";

export const buildIdVar = "OCEL_BUILD_ID";

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
