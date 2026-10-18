import { isUnderImmutablePrefix } from "@platform/edge-contract/static";
import type { StoredRecord } from "./releases";

const DEPLOYMENT_ID_PARAM = "dpl";

export function readAssetPrefix(request: Request, record: StoredRecord): string {
  const url = new URL(request.url);
  const build = url.searchParams.get(DEPLOYMENT_ID_PARAM);
  if (build === null || build === record.buildId) return record.assetPrefix;
  if (!isUnderImmutablePrefix(record.static ?? undefined, url.pathname)) return record.assetPrefix;
  const { buildAssets } = record;
  return (
    (buildAssets && Object.hasOwn(buildAssets, build) && buildAssets[build]) || record.assetPrefix
  );
}
