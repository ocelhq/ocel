import { RealtimeError } from "./client-error.js";

/** What the realtime handler answered a connect with: where the socket is and its token. */
export interface ConnectGrant {
  url: string;
  host?: string;
  token: string;
}

/** One open socket to a transport, carrying any number of subscriptions. */
export interface TransportConnection {
  /**
   * Subscribes `id` to `channel` with its subscribe token, resolving once the transport
   * accepts it and rejecting with a {@link RealtimeError} when it refuses; each event
   * then reaches `onEvent` as the envelope's JSON text.
   */
  subscribe(
    id: string,
    channel: string,
    token: string,
    onEvent: (envelope: string) => void,
  ): Promise<void>;
  unsubscribe(id: string): void;
  close(): void;
}

/** How a browser reaches one kind of transport. */
export interface ClientTransport {
  /**
   * Opens a socket with `grant`, resolving once the transport accepts it. `onDrop` hears
   * of a socket that ended without `close` being called.
   */
  connect(
    grant: ConnectGrant,
    onDrop: (error: RealtimeError) => void,
  ): Promise<TransportConnection>;
}

const transports: Record<string, () => Promise<ClientTransport>> = {
  "ocel-gateway": async () => (await import("./client-gateway.js")).gatewayTransport,
};

/** Loads the transport the realtime handler named, so a bundle carries only the one it uses. */
export async function loadTransport(name: string): Promise<ClientTransport> {
  const load = transports[name];
  if (!load) {
    // TODO(#1514): load the AppSync Events adapter for "appsync-events" once the AWS target lands.
    throw new RealtimeError(
      "unsupported-transport",
      false,
      `the realtime handler named transport "${name}", which this client does not speak`,
    );
  }
  return load();
}
