import http from "node:http";
import { installServerPreload } from "@framework/next-runtime/server-preload";
import { readEdgeCacheControlHeader } from "@platform/edge-contract/edge-cache";
import { withholdResponsesFromCloudCdn } from "./cloud-cdn.mjs";
import { newGcpNextHost } from "./next-host.mjs";

withholdResponsesFromCloudCdn(
  http.ServerResponse.prototype,
  readEdgeCacheControlHeader(process.env),
);
installServerPreload(() => newGcpNextHost(process.env));
