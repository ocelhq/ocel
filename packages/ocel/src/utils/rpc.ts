import { createClient, type Interceptor } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-node";
import { ResourceService } from "../gen/proto/app/resources/v1/resources_pb.js";
import { getDevServerToken, OCEL_DEV_SERVER } from "./constants.js";

export const authorized: Interceptor = (next) => (req) => {
  req.header.set("Authorization", `Bearer ${getDevServerToken()}`);
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

export const rpc = _rpc();
