import type { Check } from "../contract";
import { bindingChecks } from "./bindings";
import { envChecks } from "./env";
import { healthChecks } from "./health";
import { nextCacheChecks, nextDataCacheChecks } from "./nextCache";
import { nextRoutingChecks, nextStateChecks } from "./nextRouting";
import { nativeChecks, probeChecks, vendoredChecks } from "./probes";
import { productChecks } from "./product";
import { staticChecks } from "./static";

export * from "./bindings";
export * from "./env";
export * from "./health";
export * from "./nextCache";
export * from "./nextRouting";
export * from "./probes";
export * from "./product";
export * from "./static";

export const everyCheck: Check[] = [
  ...healthChecks,
  ...staticChecks,
  ...productChecks,
  ...nativeChecks,
  ...vendoredChecks,
  ...probeChecks,
  ...envChecks,
  ...bindingChecks,
  ...nextRoutingChecks,
  ...nextStateChecks,
  ...nextCacheChecks,
  ...nextDataCacheChecks,
];
