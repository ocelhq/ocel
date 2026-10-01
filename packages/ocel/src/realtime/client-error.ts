/**
 * Why the realtime handler or the transport refused an operation, or why it could not be
 * attempted: one of the handler's denial codes (`forbidden`, `unauthenticated`,
 * `no-publish-rule`, `invalid-body` and the rest), or one the client names itself:
 * `headers-failed`, `handler-unreachable`, `handler-refused`, `connection-failed`,
 * `connection-lost`, `subscribe-refused`, `unsupported-transport` or `closed`.
 */
export type RealtimeErrorCode = string;

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
