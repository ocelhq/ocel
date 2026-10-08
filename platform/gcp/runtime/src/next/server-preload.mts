import http from "node:http";
import { installServerPreload } from "@framework/next-runtime/server-preload";
import { withholdResponsesFromCloudCdn } from "./cloud-cdn.mjs";
import { newGcpNextHost } from "./next-host.mjs";

withholdResponsesFromCloudCdn(http.ServerResponse.prototype);
installServerPreload(() => newGcpNextHost(process.env));
