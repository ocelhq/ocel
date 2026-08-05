import type { Rejection, RevalidationMessage } from "./message.mjs";

// Structured logging for a secret-bearing record.
//
// The record's headers hold the app's bypass token, so nothing here takes a
// record, a header map, or an error object: `context` copies the four dedup
// ingredients out of a message and drops the rest, and every outcome is a
// closed shape whose free text is a reason code. A caller that wanted to log
// the token would have to invent a new field to carry it, which is the point —
// the rule is enforced by the types rather than by remembering it.
export interface LogContext {
  messageId: string;
  isrPrefix?: string;
  routePath?: string;
  lastModified?: number;
  enqueuedAt?: number;
}

export type FailureReason = Rejection | "timeout" | "fetch-failed" | "status-not-ok";

export type Outcome =
  | { event: "RevalidateOk" }
  | { event: "RevalidateExpectMiss"; expected: string; got: string | null }
  | { event: "RevalidateFailed"; reason: FailureReason; status?: number };

export function context(messageId: string, message: RevalidationMessage | null): LogContext {
  if (message === null) return { messageId };
  const { isrPrefix, routePath, lastModified, enqueuedAt } = message;
  return { messageId, isrPrefix, routePath, lastModified, enqueuedAt };
}

export function report(context: LogContext, outcome: Outcome): void {
  console.log(JSON.stringify({ ...outcome, ...context }));
}
