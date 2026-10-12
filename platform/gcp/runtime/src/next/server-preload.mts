import http from "node:http";
import { installServerPreload } from "@framework/next-runtime/server-preload";
import { withholdResponsesFromSharedCache } from "@framework/next-runtime/withhold-responses";
import { newGcpNextHost } from "./next-host.mjs";

withholdResponsesFromSharedCache(http.ServerResponse.prototype);
installServerPreload(() => newGcpNextHost(process.env));
