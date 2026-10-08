import http from "node:http";
import { installNextHost } from "@framework/next-runtime/host";
import { newServerAdapter } from "@framework/next-runtime/server-adapter";
import { withholdResponsesFromCloudCdn } from "./cloud-cdn.mjs";
import { newGcpNextHost } from "./next-host.mjs";

export default newServerAdapter(() => {
  withholdResponsesFromCloudCdn(http.ServerResponse.prototype);
  installNextHost(newGcpNextHost(process.env));
});
