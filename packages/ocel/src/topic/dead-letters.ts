import type { JsonText } from "../delivery/json-text.js";

/** A message a consumer gave up on after its last attempt failed. */
export interface DeadLetter {
  /** The execution that failed: the message as this consumer received it. */
  execution: string;
  /** The message's id. */
  messageId: string;
  /** When the message was sent. */
  publishedAt: Date | undefined;
  /** The message's payload. */
  payload: unknown;
  /** The JSON text the message's payload was sent as, byte for byte. */
  payloadJson: JsonText;
  /** How many attempts were made. */
  attempts: number;
  /** The last attempt's error. */
  error: string;
  /** When the last attempt failed. */
  failedAt: Date | undefined;
}

/** One page of dead letters. */
export interface DeadLetterPage {
  /** The dead letters on this page. */
  deadLetters: DeadLetter[];
  /** Passed as `cursor` to read the next page; empty on the last one. */
  nextCursor: string;
}

/** Which page of dead letters to read. */
export interface DeadLetterListOptions {
  /** The `nextCursor` of the previous page. */
  cursor?: string;
  /** The most dead letters on the page. */
  limit?: number;
}

/** The dead letters of one consumer of a topic. */
export interface DeadLetters {
  /** The consumer these dead letters belong to. */
  readonly consumer: string;
  /** One page of this consumer's dead letters. */
  list(options?: DeadLetterListOptions): Promise<DeadLetterPage>;
  /** Delivers the given executions again, or every dead letter; answers how many. */
  redrive(executions?: string[]): Promise<number>;
  /** Deletes the given executions, or every dead letter; answers how many. */
  purge(executions?: string[]): Promise<number>;
  /** How many dead letters this consumer has. */
  count(): Promise<number>;
}
