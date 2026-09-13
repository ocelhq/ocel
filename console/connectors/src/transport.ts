import {
  type Client,
  Code,
  ConnectError,
  createClient,
  type Interceptor,
} from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-node";

import { EnvVarsService } from "./gen/provider/envvars/v1/envvars_pb";
import { type Outcome, refuse, ValueError } from "./item";

export const SCOPES = ["envvars.read", "envvars.write", "envvars.reveal"] as const;

export type Scope = (typeof SCOPES)[number];

export interface Connector {
  id: string;
  url: string;
  token: string;
  capabilities: readonly string[];
}

export type Vars = Client<typeof EnvVarsService>;

function bearing(token: string): Interceptor {
  return (next) => (request) => {
    request.header.set("Authorization", `Bearer ${token}`);
    return next(request);
  };
}

export function vars(connector: Connector): Vars {
  return createClient(
    EnvVarsService,
    createConnectTransport({
      baseUrl: connector.url,
      httpVersion: "1.1",
      interceptors: [bearing(connector.token)],
    }),
  );
}

const valueCodes = new Map<Code, number>([
  [Code.FailedPrecondition, 409],
  [Code.InvalidArgument, 400],
  [Code.NotFound, 404],
  [Code.AlreadyExists, 409],
]);

export async function ask<T>(run: () => Promise<T>): Promise<Outcome<T>> {
  try {
    return { done: true, result: await run() };
  } catch (thrown) {
    if (thrown instanceof ConnectError) {
      const status = valueCodes.get(thrown.code);
      if (status !== undefined) throw new ValueError(status, thrown.rawMessage);
      if (thrown.code === Code.PermissionDenied) {
        return refuse("denied", thrown.rawMessage);
      }
      if (thrown.code === Code.Unimplemented) {
        return refuse("incompatible", thrown.rawMessage);
      }
      if (thrown.code === Code.Unauthenticated) {
        return refuse(
          "unauthenticated",
          `the connector refused the console's token: ${thrown.rawMessage}`,
        );
      }
      return refuse("offline", thrown.rawMessage);
    }
    if (thrown instanceof ValueError) throw thrown;
    return refuse("offline", thrown instanceof Error ? thrown.message : String(thrown));
  }
}

export function capabilitiesURL(url: string): string {
  const base = new URL(url);
  base.pathname = `${base.pathname.replace(/\/+$/, "")}/v1/capabilities`;
  return base.toString();
}

export async function capabilities(
  url: string,
  token: string,
): Promise<Outcome<ReadonlyArray<string>>> {
  try {
    const response = await fetch(capabilitiesURL(url), {
      headers: { Authorization: `Bearer ${token}` },
    });
    if (response.status === 401 || response.status === 403) {
      return refuse(
        "unauthenticated",
        `the connector refused the console's token with ${response.status}`,
      );
    }
    if (!response.ok) {
      return refuse("incompatible", `the connector answered ${response.status}`);
    }
    const read = (await response.json()) as { capabilities?: string[] };
    return { done: true, result: read.capabilities ?? [] };
  } catch (thrown) {
    return refuse("offline", thrown instanceof Error ? thrown.message : String(thrown));
  }
}
