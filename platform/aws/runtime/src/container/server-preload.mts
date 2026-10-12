import http from "node:http";
import { installServerPreload } from "@framework/next-runtime/server-preload";
import { withholdResponsesFromSharedCache } from "@framework/next-runtime/withhold-responses";
import { newAwsNextHost } from "../next/next-host.mjs";

withholdResponsesFromSharedCache(http.ServerResponse.prototype);
installServerPreload(() => newAwsNextHost(process.env));
