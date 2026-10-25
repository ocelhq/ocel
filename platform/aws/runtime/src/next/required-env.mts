import { readStorageCoordinate, storageCoordinateVar } from "@platform/edge-contract/runtime-env";
import type { StorageCoordinate } from "@platform/edge-contract/storage-key";

export function requireEnv(name: string): string {
  const value = process.env[name];
  if (!value) throw new Error(`ocel cache: ${name} is not set`);
  return value;
}

export function requireStorageCoordinate(): StorageCoordinate {
  const coordinate = readStorageCoordinate(process.env);
  if (coordinate === undefined) throw new Error(`ocel cache: ${storageCoordinateVar} is not set`);
  return coordinate;
}
