import { isUnderImmutablePrefix } from "@platform/edge-contract/static";
import { assetPrefix, parseStorageCoordinate } from "@platform/edge-contract/storage-key";
import type { StoredRecord } from "./releases";

const DEPLOYMENT_ID_PARAM = "dpl";

function assetPrefixAt(storageCoordinate: string): string {
  const coordinate = parseStorageCoordinate(storageCoordinate);
  return coordinate === undefined ? "" : assetPrefix(coordinate);
}

export function recordAssetPrefix(record: Pick<StoredRecord, "storageCoordinate">): string {
  return assetPrefixAt(record.storageCoordinate);
}

export function readAssetPrefix(request: Request, record: StoredRecord): string {
  const url = new URL(request.url);
  const build = url.searchParams.get(DEPLOYMENT_ID_PARAM);
  if (build === null || build === record.buildId) return recordAssetPrefix(record);
  if (!isUnderImmutablePrefix(record.static ?? undefined, url.pathname)) {
    return recordAssetPrefix(record);
  }
  const { buildCoordinates } = record;
  const earlier =
    buildCoordinates && Object.hasOwn(buildCoordinates, build) && buildCoordinates[build];
  return earlier ? assetPrefixAt(earlier) : recordAssetPrefix(record);
}
