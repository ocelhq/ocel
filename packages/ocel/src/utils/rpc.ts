import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-node";
import {
  ResourceService,
  ResourceType,
} from "../gen/proto/resources/v1/resources_pb";
import { OCEL_DEV_SERVER } from "./constants";

const _rpc = () => {
  /**
   * validation for this happens in the `defer` function since we can be sure it's only called during dev mode
   */
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

export { ResourceType };
