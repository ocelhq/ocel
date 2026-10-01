import type { RealtimeDenial } from "./handler.js";

/**
 * Why the realtime handler or the transport refused an operation, or why it could not be
 * attempted: one of the handler's denial codes, or one the client names itself.
 *
 * - `headers-failed`: the `headers` function threw.
 * - `handler-unreachable`: the handler could not be reached, did not answer in time, or
 *   answered without JSON.
 * - `handler-refused`: the handler answered with an HTTP error status.
 * - `invalid-grant`: the handler granted a subscribe without the token it takes.
 * - `connection-failed`: the socket could not be opened, or was not accepted in time.
 * - `connection-lost`: the open socket dropped or went silent.
 * - `subscribe-refused`: the transport refused a subscription the handler granted.
 * - `unsupported-transport`: the handler named a transport this client does not speak.
 * - `closed`: the client was closed.
 */
export type RealtimeErrorCode =
  | RealtimeDenial
  | "headers-failed"
  | "handler-unreachable"
  | "handler-refused"
  | "invalid-grant"
  | "connection-failed"
  | "connection-lost"
  | "subscribe-refused"
  | "unsupported-transport"
  | "closed";

/** An operation the realtime client could not complete. */
export class RealtimeError extends Error {
  override name = "RealtimeError";
  /** What refused the operation, or why it could not be attempted. */
  readonly code: RealtimeErrorCode;
  /**
   * Whether the same operation may succeed if tried again: true for a network failure, a
   * dropped connection or a failing rule, false for a denial that would be answered the
   * same way. A subscription the client keeps retrying on its own reports `true`.
   */
  readonly retriable: boolean;

  constructor(code: RealtimeErrorCode, retriable: boolean, message: string) {
    super(message);
    this.code = code;
    this.retriable = retriable;
  }
}
