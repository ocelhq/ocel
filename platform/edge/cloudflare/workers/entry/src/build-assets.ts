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
): StorageCoordinate | undefined {
  const url = new URL(request.url);
  const build = url.searchParams.get(DEPLOYMENT_ID_PARAM);
  if (build === null || build === record.buildId) return storage;
  if (!isUnderImmutablePrefix(record.static ?? undefined, url.pathname)) return storage;
  const { buildCoordinates } = record;
  if (!buildCoordinates || !Object.hasOwn(buildCoordinates, build)) return storage;
  const earlier = parseStorageCoordinate(buildCoordinates[build] ?? "");
  if (!earlier) {
    console.error(
      `ocel: release ${record.release} records build ${build} at ${JSON.stringify(buildCoordinates[build])}, which is no storage coordinate; its assets are not found`,
    );
  }
  return earlier;
}
