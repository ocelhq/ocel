import http from "node:http";
import { dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { installNextHost } from "@framework/next-runtime/host";
import { newServerAdapter } from "@framework/next-runtime/server-adapter";
import { withholdResponsesFromCloudCdn } from "./cloud-cdn.mjs";
import { newGcpNextHost } from "./next-host.mjs";

export default newServerAdapter(dirname(fileURLToPath(import.meta.url)), () => {
  withholdResponsesFromCloudCdn(http.ServerResponse.prototype);
  installNextHost(newGcpNextHost(process.env));
});
