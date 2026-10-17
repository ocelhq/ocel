import type { Interceptor, Transport } from "@connectrpc/connect";
import { createConnectTransport as createNodeConnectTransport } from "@connectrpc/connect-node";

/** Where a Connect transport sends its calls, and the interceptors every call passes through. */
export interface ConnectTransportOptions {
  baseUrl: string;
  interceptors: Interceptor[];
}

/** A Connect transport over node's HTTP/1.1 client. */
export function createConnectTransport(options: ConnectTransportOptions): Transport {
  return createNodeConnectTransport({ httpVersion: "1.1", ...options });
}
