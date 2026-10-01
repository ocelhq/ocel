import { RealtimeError } from "./client-error.js";
import type { ClientTransport, TransportConnection } from "./client-transport.js";

const subprotocol = "aws-appsync-event-ws";
const answerDeadlineMilliseconds = 10_000;

interface GatewayFrame {
  type: string;
  id?: string;
  event?: string;
  connectionTimeoutMs?: number;
  errors?: { errorType?: string; message?: string }[];
}

interface PendingSubscribe {
  resolve(): void;
  reject(error: RealtimeError): void;
  deadline: ReturnType<typeof setTimeout>;
  isCancelled: boolean;
}

function encodeBase64url(text: string): string {
  let binary = "";
  for (const byte of new TextEncoder().encode(text)) binary += String.fromCharCode(byte);
  return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

function describeErrors(frame: GatewayFrame): string {
  return (frame.errors ?? []).map((error) => error.message ?? error.errorType ?? "").join("; ");
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
      const pending = new Map<string, PendingSubscribe>();
      const listeners = new Map<string, (envelope: string) => void>();
      let isOpen = false;
      let hasEnded = false;
      let isClosedByClient = false;
      let silenceTimer: ReturnType<typeof setTimeout> | undefined;
      let silenceLimitMilliseconds = 0;

      const send = (frame: unknown) => {
        if (socket.readyState === WebSocket.OPEN) socket.send(JSON.stringify(frame));
      };
      const end = (error: RealtimeError) => {
        if (hasEnded) return;
        hasEnded = true;
        clearTimeout(handshakeDeadline);
        clearTimeout(silenceTimer);
        for (const waiting of pending.values()) {
          clearTimeout(waiting.deadline);
          waiting.reject(error);
        }
        pending.clear();
        listeners.clear();
        if (!isOpen) {
          reject(error);
          return;
        }
        isOpen = false;
        if (!isClosedByClient) onDrop(error);
      };
      const abandon = (error: RealtimeError) => {
        socket.close();
        end(error);
      };
      const handshakeDeadline = setTimeout(
        () =>
          abandon(
            new RealtimeError(
              "connection-failed",
              true,
              `the gateway did not accept the connection within ${answerDeadlineMilliseconds}ms`,
            ),
          ),
        answerDeadlineMilliseconds,
      );
      const watchSilence = () => {
        if (silenceLimitMilliseconds <= 0) return;
        clearTimeout(silenceTimer);
        silenceTimer = setTimeout(
          () =>
            abandon(
              new RealtimeError(
                "connection-lost",
                true,
                `the gateway sent nothing for ${silenceLimitMilliseconds}ms`,
              ),
            ),
          silenceLimitMilliseconds,
        );
      };
      const settleSubscribe = (id: string) => {
        const waiting = pending.get(id);
        pending.delete(id);
        if (waiting) clearTimeout(waiting.deadline);
        return waiting;
      };

      const connection: TransportConnection = {
        subscribe(id, channel, token, onEvent) {
          if (!isOpen) {
            return Promise.reject(
              new RealtimeError("connection-lost", true, "the connection is no longer open"),
            );
          }
          return new Promise<void>((subscribed, refused) => {
            const waiting: PendingSubscribe = {
              resolve: () => {
                if (waiting.isCancelled) send({ type: "unsubscribe", id });
                else listeners.set(id, onEvent);
                subscribed();
              },
              reject: refused,
              deadline: setTimeout(
                () =>
                  abandon(
                    new RealtimeError(
                      "connection-lost",
                      true,
                      `the gateway did not answer a subscribe within ${answerDeadlineMilliseconds}ms`,
                    ),
                  ),
                answerDeadlineMilliseconds,
              ),
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
            clearTimeout(handshakeDeadline);
            isOpen = true;
            silenceLimitMilliseconds = frame.connectionTimeoutMs ?? 0;
            watchSilence();
            resolve(connection);
            return;
          case "connection_error":
            abandon(
              new RealtimeError(
                "connection-failed",
                true,
                `the gateway refused the connection: ${describeErrors(frame)}`,
              ),
            );
            return;
          case "subscribe_success":
            settleSubscribe(frame.id ?? "")?.resolve();
            return;
          case "subscribe_error":
            settleSubscribe(frame.id ?? "")?.reject(
              new RealtimeError(
                "subscribe-refused",
                false,
                `the gateway refused the subscription: ${describeErrors(frame)}`,
              ),
            );
            return;
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
