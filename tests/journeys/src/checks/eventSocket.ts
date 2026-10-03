import assert from "node:assert/strict";
import { setTimeout as delay } from "node:timers/promises";

const SUBPROTOCOL = "aws-appsync-event-ws";
const ANSWER_WITHIN_MS = 10_000;

type SocketFrame = {
  type: string;
  id?: string;
  event?: unknown;
  errors?: { errorType?: string; message?: string }[];
};

export type Envelope = {
  v: number;
  id: string;
  ch: string;
  ts: number;
  kind: string;
  data: unknown;
};

export class SocketRefusal extends Error {
  readonly errorType: string;

  constructor(frame: SocketFrame) {
    const [first] = frame.errors ?? [];
    super(`${frame.type}: ${first?.errorType ?? "no error type"}: ${first?.message ?? ""}`);
    this.errorType = first?.errorType ?? "";
  }
}

function buildAuthorization(token: string, host: string | undefined) {
  return host === undefined ? { Authorization: token } : { host, Authorization: token };
}

function encodeAuthorization(token: string, host: string | undefined): string {
  return `header-${Buffer.from(JSON.stringify(buildAuthorization(token, host))).toString("base64url")}`;
}

function readEnvelopes(event: unknown): Envelope[] {
  const texts = Array.isArray(event) ? event : [event];
  return texts.map((text) => {
    const decoded: unknown = JSON.parse(String(text));
    return (typeof decoded === "string" ? JSON.parse(decoded) : decoded) as Envelope;
  });
}

export class EventSocket {
  readonly #socket: WebSocket;
  readonly #host: string | undefined;
  readonly #frames: SocketFrame[] = [];
  readonly #waiters = new Set<() => void>();
  #closed = false;

  private constructor(socket: WebSocket, host: string | undefined) {
    this.#socket = socket;
    this.#host = host;
    socket.addEventListener("message", (message) => {
      this.#frames.push(JSON.parse(String(message.data)) as SocketFrame);
      this.#wake();
    });
    socket.addEventListener("close", () => {
      this.#closed = true;
      this.#wake();
    });
  }

  static async open(url: string, host: string | undefined, token: string): Promise<EventSocket> {
    const socket = new EventSocket(
      new WebSocket(url, [SUBPROTOCOL, encodeAuthorization(token, host)]),
      host,
    );
    await new Promise<void>((resolve, reject) => {
      socket.#socket.addEventListener("open", () => resolve(), { once: true });
      socket.#socket.addEventListener(
        "close",
        () => reject(new Error(`${url} closed before it opened`)),
        { once: true },
      );
    }).catch((error: unknown) => {
      const refused = socket.#frames.find((frame) => frame.type === "connection_error");
      throw refused ? new SocketRefusal(refused) : error;
    });
    socket.#socket.send(JSON.stringify({ type: "connection_init" }));
    const answer = await socket.#waitForFrame(
      (frame) => frame.type === "connection_ack" || frame.type === "connection_error",
      "an answer to connection_init",
    );
    if (answer.type === "connection_error") {
      socket.close();
      throw new SocketRefusal(answer);
    }
    return socket;
  }

  async subscribe(id: string, channel: string, token: string): Promise<void> {
    this.#socket.send(
      JSON.stringify({
        type: "subscribe",
        id,
        channel,
        authorization: buildAuthorization(token, this.#host),
      }),
    );
    const answer = await this.#waitForFrame(
      (frame) =>
        frame.id === id && (frame.type === "subscribe_success" || frame.type === "subscribe_error"),
      `an answer to subscribe ${id} on ${channel}`,
    );
    if (answer.type === "subscribe_error") {
      throw new SocketRefusal(answer);
    }
  }

  async readNextEvent(id: string, withinMs = ANSWER_WITHIN_MS): Promise<Envelope> {
    const frame = await this.#waitForFrame(
      (one) => one.type === "data" && one.id === id,
      `an event on subscription ${id}`,
      withinMs,
    );
    this.#frames.splice(this.#frames.indexOf(frame), 1);
    const [envelope] = readEnvelopes(frame.event);
    assert.ok(envelope, `the data frame on subscription ${id} carried no event`);
    return envelope;
  }

  async readEventsWithin(id: string, ms: number): Promise<Envelope[]> {
    await delay(ms);
    return this.#frames
      .filter((frame) => frame.type === "data" && frame.id === id)
      .flatMap((frame) => readEnvelopes(frame.event));
  }

  close(): void {
    this.#socket.close(1000);
  }

  #wake(): void {
    for (const waiter of this.#waiters) {
      waiter();
    }
  }

  async #waitForFrame(
    matches: (frame: SocketFrame) => boolean,
    what: string,
    withinMs = ANSWER_WITHIN_MS,
  ): Promise<SocketFrame> {
    const deadline = Date.now() + withinMs;
    for (;;) {
      const found = this.#frames.find(matches);
      if (found) {
        return found;
      }
      if (this.#closed) {
        throw new Error(`the socket closed before ${what}`);
      }
      const left = deadline - Date.now();
      if (left <= 0) {
        throw new Error(`no ${what} within ${withinMs}ms`);
      }
      await new Promise<void>((resolve) => {
        const wake = () => {
          clearTimeout(timer);
          this.#waiters.delete(wake);
          resolve();
        };
        const timer = setTimeout(wake, left);
        this.#waiters.add(wake);
      });
    }
  }
}
