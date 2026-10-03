import {
  type RealtimeProperties,
  RealtimeTransport,
} from "../gen/proto/common/bindings/v1/bindings_pb.js";
import { findAppSyncRegion, readAwsCredentials, signAppSyncPublish } from "./aws-signature.js";
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

function hasFailedEvents(answer: string): boolean {
  try {
    const failed = (JSON.parse(answer) as { failed?: unknown[] }).failed;
    return Array.isArray(failed) && failed.length > 0;
  } catch {
    return false;
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
        signPublish(channel) {
          return async (envelope) => {
            const failed = (said: string, cause?: unknown) =>
              new Error(`realtime("${issuer.name}"): publish on ${channel}: ${said}`, { cause });
            const body = JSON.stringify({ channel, events: [envelope] });
            const credentials = await readAwsCredentials().catch((cause: Error) => {
              throw failed(cause.message, cause);
            });
            const headers = signAppSyncPublish(
              properties.host,
              body,
              credentials,
              findAppSyncRegion(properties.host),
              new Date(),
            );
            let response: Response;
            try {
              response = await postWithDeadline(`https://${properties.host}/event`, {
                method: "POST",
                headers,
                body,
              });
            } catch (cause) {
              throw failed(
                `AppSync did not take it within ${publishDeadlineMilliseconds / 1_000}s`,
                cause,
              );
            }
            const answer = await response.text();
            if (!response.ok) {
              throw failed(`AppSync refused it with status ${response.status}: ${answer}`);
            }
            if (hasFailedEvents(answer)) throw failed(`AppSync failed the event: ${answer}`);
          };
        },
      };
    default:
      throw new Error(
        `realtime("${issuer.name}"): the binding names transport ${properties.transport}, which this SDK does not speak`,
      );
  }
}
