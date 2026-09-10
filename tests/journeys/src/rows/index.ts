import type { ContractRow } from "../contract";
import { bindingRows } from "./bindings";
import { envRows } from "./env";
import { healthRows } from "./health";
import { nextCacheRows, nextDataCacheRows } from "./nextCache";
import { nextRoutingRows, nextStateRows } from "./nextRouting";
import { nativeRows, probeRows, vendoredRows } from "./probes";
import { productRows } from "./product";
import { staticRows } from "./static";

export * from "./bindings";
export * from "./env";
export * from "./health";
export * from "./nextCache";
export * from "./nextRouting";
export * from "./probes";
export * from "./product";
export * from "./static";

export const everyRow: ContractRow[] = [
  ...healthRows,
  ...staticRows,
  ...productRows,
  ...nativeRows,
  ...vendoredRows,
  ...probeRows,
  ...envRows,
  ...bindingRows,
  ...nextRoutingRows,
  ...nextStateRows,
  ...nextCacheRows,
  ...nextDataCacheRows,
];
