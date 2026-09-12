import { type Client, Code, ConnectError, createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-node";

import { EnvVarsService } from "./gen/provider/envvars/v1/envvars_pb";
import { type Outcome, refuse, ValueError } from "./item";

export interface Connector {
  id: string;
  url: string;
  capabilities: readonly string[];
}

export type Vars = Client<typeof EnvVarsService>;

// TODO(alpha): #1134 has the connector polling the console for work items rather than
// answering a URL the console dials. Every caller already awaits an Outcome, so the
// change lands here and nowhere else.
export function vars(connector: Connector): Vars {
  return createClient(
    EnvVarsService,
    createConnectTransport({ baseUrl: connector.url, httpVersion: "1.1" }),
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
      return refuse("offline", thrown.rawMessage);
    }
    if (thrown instanceof ValueError) throw thrown;
    return refuse("offline", thrown instanceof Error ? thrown.message : String(thrown));
  }
}

export async function capabilities(url: string): Promise<Outcome<ReadonlyArray<string>>> {
  try {
    const response = await fetch(new URL("/v1/capabilities", url));
    if (!response.ok) {
      return refuse("incompatible", `the connector answered ${response.status}`);
    }
    const read = (await response.json()) as { capabilities?: string[] };
    return { done: true, result: read.capabilities ?? [] };
  } catch (thrown) {
    return refuse("offline", thrown instanceof Error ? thrown.message : String(thrown));
  }
}
