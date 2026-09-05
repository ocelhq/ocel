import type { ContractRow } from "../contract";
import { envRows } from "./env";
import { healthRows } from "./health";
import { linkRows } from "./links";
import { nextCacheRows, nextDataCacheRows } from "./nextCache";
import { nextRoutingRows, nextStateRows } from "./nextRouting";
import { probeRows } from "./probes";
import { productRows } from "./product";
import { staticRows } from "./static";

export { ENV_ROW, envRows, setsEnv } from "./env";
export { healthRows } from "./health";
export { LINK_QUERY_ROW, LINK_ROW, linkRows } from "./links";
export { EDGE_ISR_TITLE, nextCacheRows, nextDataCacheRows } from "./nextCache";
export { nextRoutingRows, nextStateRows } from "./nextRouting";
export { probeRows, STREAM_ROW } from "./probes";
export { migrates, productRows, UPLOAD_ROW } from "./product";
export { staticRows } from "./static";

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
