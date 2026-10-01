import type { Interceptor, Transport } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-node";
import { getRuntimeAddress, getSessionToken } from "../utils/get-config.js";

function createAuthorizationInterceptor(token: string): Interceptor {
  return (next) => (req) => {
    req.header.set("Authorization", `Bearer ${token}`);
    return next(req);
  };
}

export function createRuntimeTransport(): Transport {
  return createConnectTransport({
    httpVersion: "1.1",
    baseUrl: getRuntimeAddress(),
    interceptors: [createAuthorizationInterceptor(getSessionToken())],
  });
}
