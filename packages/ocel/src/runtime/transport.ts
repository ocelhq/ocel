import type { Interceptor, Transport } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-node";

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
    httpVersion: "1.1",
    baseUrl: getRuntimeAddress(),
    interceptors: [createAuthorizationInterceptor(getSessionToken())],
  });
}
