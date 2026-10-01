import {
  type RealtimeProperties,
  RealtimeTransport,
} from "../gen/proto/common/bindings/v1/bindings_pb.js";
import { mintToken, type TokenIssuer } from "./token.js";

/** The transport a realtime binding names: how the handler names it and how events reach it. */
export interface Transport {
  name: "appsync-events" | "ocel-gateway";
  host?: string;
  /**
   * Mints what publishing on `channel` takes, throwing when it cannot be minted, and
   * answers the function that sends one envelope there.
   */
  signPublish(channel: string): (envelope: string) => Promise<void>;
}

const publishDeadlineMilliseconds = 10_000;

function buildGatewayPublishURL(socketURL: string): string {
  const url = new URL(socketURL);
  url.protocol = url.protocol === "wss:" ? "https:" : "http:";
  url.pathname = "/publish";
  url.search = "";
  return url.toString();
}

async function postWithDeadline(url: string, init: RequestInit): Promise<Response> {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), publishDeadlineMilliseconds);
  try {
    return await fetch(url, { ...init, signal: controller.signal });
  } finally {
    clearTimeout(timer);
  }
}

/**
 * Resolves the transport `properties` names for `issuer`. Throws for a transport this SDK
 * does not speak.
 */
export function resolveTransport(properties: RealtimeProperties, issuer: TokenIssuer): Transport {
  switch (properties.transport) {
    case RealtimeTransport.OCEL_GATEWAY:
      return {
        name: "ocel-gateway",
        signPublish(channel) {
          const { token } = mintToken(properties, issuer, "server", "publish", channel);
          return async (envelope) => {
            let response: Response;
            try {
              response = await postWithDeadline(buildGatewayPublishURL(properties.url), {
                method: "POST",
                headers: { authorization: `Bearer ${token}`, "content-type": "application/json" },
                body: envelope,
              });
            } catch (cause) {
              throw new Error(
                `realtime("${issuer.name}"): the gateway did not take a publish on ${channel} within ${publishDeadlineMilliseconds / 1_000}s`,
                { cause },
              );
            }
            if (!response.ok) {
              throw new Error(
                `realtime("${issuer.name}"): the gateway refused a publish on ${channel} with status ${response.status}`,
              );
            }
          };
        },
      };
    case RealtimeTransport.APPSYNC_EVENTS:
      return {
        name: "appsync-events",
        host: properties.host,
        signPublish() {
          return async () => {
            // TODO(#1514): publish with a SigV4-signed POST /event under the app's role once the AWS target lands.
            throw new Error(
              `realtime("${issuer.name}"): publishing on AppSync Events is not supported yet`,
            );
          };
        },
      };
    default:
      throw new Error(
        `realtime("${issuer.name}"): the binding names transport ${properties.transport}, which this SDK does not speak`,
      );
  }
}
