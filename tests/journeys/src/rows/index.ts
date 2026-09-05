import type { ContractRow } from "../contract";
import { envRows } from "./env";
import { healthRows } from "./health";
import { linkRows } from "./links";
import { nextCacheRows, nextDataCacheRows } from "./nextCache";
import { nextRoutingRows, nextStateRows } from "./nextRouting";
import { probeRows } from "./probes";
import { productRows } from "./product";
import { staticRows } from "./static";

export * from "./env";
export * from "./health";
export * from "./links";
export * from "./nextCache";
export * from "./nextRouting";
export * from "./probes";
export * from "./product";
export * from "./static";

export const everyRow: ContractRow[] = [
  ...healthRows,
  ...staticRows,
  ...productRows,
  ...probeRows,
  ...envRows,
  ...linkRows,
  ...nextRoutingRows,
  ...nextStateRows,
  ...nextCacheRows,
  ...nextDataCacheRows,
];
