import {
  type RealtimeProperties,
  RealtimeTransport,
} from "../gen/proto/common/bindings/v1/bindings_pb.js";

/** The transport a realtime binding names, as the handler names it to a browser. */
export interface Transport {
  name: "appsync-events" | "ocel-gateway";
  host?: string;
}

/**
 * Resolves the transport `properties` names for the resource `name`. Throws for a
 * transport this SDK does not speak.
 */
export function resolveTransport(properties: RealtimeProperties, name: string): Transport {
  switch (properties.transport) {
    case RealtimeTransport.OCEL_GATEWAY:
      return { name: "ocel-gateway" };
    case RealtimeTransport.APPSYNC_EVENTS:
      return { name: "appsync-events", host: properties.host };
    default:
      throw new Error(
        `realtime("${name}"): the binding names transport ${properties.transport}, which this SDK does not speak`,
      );
  }
}
