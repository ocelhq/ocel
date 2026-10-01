import { RealtimeError } from "./client-error.js";
import type { ClientTransport, TransportConnection } from "./client-transport.js";

const subprotocol = "aws-appsync-event-ws";

interface GatewayFrame {
  type: string;
  id?: string;
  event?: string;
  connectionTimeoutMs?: number;
  errors?: { errorType?: string; message?: string }[];
}

function encodeBase64url(text: string): string {
  let binary = "";
  for (const byte of new TextEncoder().encode(text)) binary += String.fromCharCode(byte);
  return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

function describeErrors(frame: GatewayFrame): string {
  return (frame.errors ?? []).map((e) => e.message ?? e.errorType ?? "").join("; ");
}

/** The Ocel gateway's socket, which speaks the AppSync Events protocol. */
export const gatewayTransport: ClientTransport = {
  connect(grant, onDrop) {
    return new Promise<TransportConnection>((resolve, reject) => {
      const authorization = encodeBase64url(
        JSON.stringify(
          grant.host === undefined
            ? { Authorization: grant.token }
            : { host: grant.host, Authorization: grant.token },
        ),
      );
      const socket = new WebSocket(grant.url, [subprotocol, `header-${authorization}`]);
      const pending = new Map<
        string,
        { resolve(): void; reject(error: RealtimeError): void; isCancelled: boolean }
      >();
      const listeners = new Map<string, (envelope: string) => void>();
      let isOpen = false;
      let isClosedByClient = false;
      let silenceTimer: ReturnType<typeof setTimeout> | undefined;
      let silenceLimitMs = 0;

      const send = (frame: unknown) => {
        if (socket.readyState === WebSocket.OPEN) socket.send(JSON.stringify(frame));
      };
      const end = (error: RealtimeError) => {
        clearTimeout(silenceTimer);
        for (const waiting of pending.values()) waiting.reject(error);
        pending.clear();
        listeners.clear();
        if (!isOpen) {
          reject(error);
          return;
        }
        isOpen = false;
        if (!isClosedByClient) onDrop(error);
      };
      const watchSilence = () => {
        if (silenceLimitMs <= 0) return;
        clearTimeout(silenceTimer);
        silenceTimer = setTimeout(() => {
          socket.close();
          end(
            new RealtimeError(
              "connection-lost",
              true,
              `the gateway sent nothing for ${silenceLimitMs}ms`,
            ),
          );
        }, silenceLimitMs);
      };

      const connection: TransportConnection = {
        subscribe(id, channel, token, onEvent) {
          if (!isOpen) {
            return Promise.reject(
              new RealtimeError("connection-lost", true, "the connection is no longer open"),
            );
          }
          return new Promise<void>((subscribed, refused) => {
            const waiting = {
              resolve: () => {
                if (waiting.isCancelled) send({ type: "unsubscribe", id });
                else listeners.set(id, onEvent);
                subscribed();
              },
              reject: refused,
              isCancelled: false,
            };
            pending.set(id, waiting);
            send({ type: "subscribe", id, channel, authorization: { Authorization: token } });
          });
        },
        unsubscribe(id) {
          const waiting = pending.get(id);
          if (waiting) waiting.isCancelled = true;
          if (listeners.delete(id)) send({ type: "unsubscribe", id });
        },
        close() {
          isClosedByClient = true;
          socket.close(1000);
          end(new RealtimeError("closed", false, "the realtime client was closed"));
        },
      };

      socket.addEventListener("open", () => send({ type: "connection_init" }));
      socket.addEventListener("message", (message) => {
        let frame: GatewayFrame;
        try {
          frame = JSON.parse(String(message.data));
        } catch {
          return;
        }
        watchSilence();
        switch (frame.type) {
          case "connection_ack":
            isOpen = true;
            silenceLimitMs = frame.connectionTimeoutMs ?? 0;
            watchSilence();
            resolve(connection);
            return;
          case "connection_error":
            end(
              new RealtimeError(
                "connection-failed",
                true,
                `the gateway refused the connection: ${describeErrors(frame)}`,
              ),
            );
            socket.close();
            return;
          case "subscribe_success": {
            const waiting = pending.get(frame.id ?? "");
            pending.delete(frame.id ?? "");
            waiting?.resolve();
            return;
          }
          case "subscribe_error": {
            const waiting = pending.get(frame.id ?? "");
            pending.delete(frame.id ?? "");
            waiting?.reject(
              new RealtimeError(
                "subscribe-refused",
                false,
                `the gateway refused the subscription: ${describeErrors(frame)}`,
              ),
            );
            return;
          }
          case "data":
            if (frame.event !== undefined) listeners.get(frame.id ?? "")?.(frame.event);
            return;
        }
      });
      socket.addEventListener("close", () =>
        end(
          new RealtimeError(
            isOpen ? "connection-lost" : "connection-failed",
            true,
            isOpen ? "the connection to the gateway closed" : "the gateway could not be reached",
          ),
        ),
      );
    });
  },
};
