import type { DescMethod, DescService } from "@bufbuild/protobuf";
import {
  type Client,
  Code,
  ConnectError,
  createClient,
  type Interceptor,
  type Transport,
} from "@connectrpc/connect";
import { createConnectTransport } from "#connect-transport";
import { RuntimeCallError } from "./errors.js";

/** The env key the ocel runtime's address is delivered under. */
export const RUNTIME_ADDRESS = "OCEL_RUNTIME_ADDRESS";

/** The env key the token every call to the ocel runtime presents is delivered under. */
export const SESSION_TOKEN = "OCEL_SESSION_TOKEN";

/** The delivered address of the ocel runtime. Throws when none was delivered. */
export const getRuntimeAddress = () => {
  const address = process.env[RUNTIME_ADDRESS];

  if (!address) {
    throw new Error(
      `${RUNTIME_ADDRESS} is not defined, so no resource the ocel runtime serves can be reached. Run \`ocel dev\` to serve it locally, or \`ocel deploy\` to have the deployed runtime's address delivered.`,
    );
  }

  return address;
};

/** The delivered session token for the ocel runtime. Throws when none was delivered. */
export const getSessionToken = () => {
  const token = process.env[SESSION_TOKEN];

  if (!token) {
    throw new Error(
      `${SESSION_TOKEN} is not defined, so the ocel runtime at ${RUNTIME_ADDRESS} would refuse every call. It is delivered beside ${RUNTIME_ADDRESS} by \`ocel dev\` and by the deployed runtime, never set by hand.`,
    );
  }

  return token;
};

function createAuthorizationInterceptor(token: string): Interceptor {
  return (next) => (req) => {
    req.header.set("Authorization", `Bearer ${token}`);
    return next(req);
  };
}

/** A transport to the ocel runtime that presents the session token on every call. Throws when the address or token was not delivered. */
export function createRuntimeTransport(): Transport {
  return createConnectTransport({
    baseUrl: getRuntimeAddress(),
    interceptors: [createAuthorizationInterceptor(getSessionToken())],
  });
}

function wireCode(code: Code): string {
  return (Code[code] ?? "Unknown").replace(/[A-Z]/g, (letter, at: number) =>
    at === 0 ? letter.toLowerCase() : `_${letter.toLowerCase()}`,
  );
}

function refusal(service: DescService, method: DescMethod, cause: unknown): RuntimeCallError {
  const error = ConnectError.from(cause);
  return new RuntimeCallError(
    `${service.typeName}/${method.name}`,
    wireCode(error.code),
    error.rawMessage,
    error,
  );
}

/** A client of the ocel runtime whose every failed call throws a {@link RuntimeCallError}. Throws when the address or token was not delivered. */
export function createRuntimeClient<TService extends DescService>(
  service: TService,
): Client<TService> {
  const client = createClient(service, createRuntimeTransport()) as Record<
    string,
    (...args: unknown[]) => Promise<unknown>
  >;
  const refusing: Record<string, (...args: unknown[]) => Promise<unknown>> = {};
  for (const method of service.methods) {
    const call = client[method.localName];
    if (!call) continue;
    refusing[method.localName] = async (...args) => {
      try {
        return await call(...args);
      } catch (cause) {
        throw refusal(service, method, cause);
      }
    };
  }
  return refusing as Client<TService>;
}
