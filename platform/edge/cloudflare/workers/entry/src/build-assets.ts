import { isUnderImmutablePrefix } from "@platform/edge-contract/static";
import {
  parseStorageCoordinate,
  type StorageCoordinate,
} from "@platform/edge-contract/storage-key";
import type { StoredRecord } from "./releases";

const DEPLOYMENT_ID_PARAM = "dpl";

export function readAssetStorage(
  request: Request,
  record: StoredRecord,
  storage: StorageCoordinate,
): StorageCoordinate {
  const url = new URL(request.url);
  const build = url.searchParams.get(DEPLOYMENT_ID_PARAM);
  if (build === null || build === record.buildId) return storage;
  if (!isUnderImmutablePrefix(record.static ?? undefined, url.pathname)) return storage;
  const { buildCoordinates } = record;
  const earlier =
    buildCoordinates && Object.hasOwn(buildCoordinates, build) && buildCoordinates[build];
  return (earlier && parseStorageCoordinate(earlier)) || storage;
}
