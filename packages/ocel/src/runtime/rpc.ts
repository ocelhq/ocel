import { createClient, type Interceptor } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-node";
import { ResourceService } from "../gen/proto/app/resources/v1/resources_pb.js";
import { SDK_VERSION } from "../version.js";
import { getDevServerToken, OCEL_DEV_SERVER, SDK_VERSION_HEADER } from "./dev-server.js";

/** Presents the dev-server token and this SDK's version on every call. */
export const authorized: Interceptor = (next) => (req) => {
  req.header.set("Authorization", `Bearer ${getDevServerToken()}`);
  req.header.set(SDK_VERSION_HEADER, `js/${SDK_VERSION}`);
  return next(req);
};

const _rpc = () => {
  const transport = createConnectTransport({
    httpVersion: "1.1",
    baseUrl: OCEL_DEV_SERVER!,
    interceptors: [authorized],
  });

  const resourceClient = createClient(ResourceService, transport);

  return {
    resource: resourceClient,
  };
};

/** Clients for the services the dev server serves. */
export const rpc = _rpc();
