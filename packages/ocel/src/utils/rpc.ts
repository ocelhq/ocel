import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-node";
import { ResourceService } from "../gen/proto/app/resources/v1/resources_pb.js";
import { OCEL_DEV_SERVER } from "./constants.js";

const _rpc = () => {
  const transport = createConnectTransport({
    httpVersion: "1.1",
    baseUrl: OCEL_DEV_SERVER!,
  });

  const resourceClient = createClient(ResourceService, transport);

  return {
    resource: resourceClient,
  };
};

export const rpc = _rpc();
