import type { Rejection, RevalidationMessage } from "@platform/edge-contract/revalidation";
import type { ResolveFailure } from "./origin.mjs";

export interface LogContext {
  messageId: string;
  isrPrefix?: string;
  routePath?: string;
  lastModified?: number;
  enqueuedAt?: number;
}

export type FailureReason =
  | Rejection
  | ResolveFailure
  | "timeout"
  | "fetch-failed"
  | "status-not-ok"
  | "handler-error";

export type Outcome =
  | { event: "RevalidateOk" }
  | { event: "RevalidateExpectMiss"; expected: string; got: string | null }
  | { event: "RevalidateSkipped"; reason: "group-stopped" }
  | { event: "RevalidateFailed"; reason: FailureReason; status?: number };

export function context(messageId: string, message: RevalidationMessage | null): LogContext {
  if (message === null) return { messageId };
  const { isrPrefix, routePath, lastModified, enqueuedAt } = message;
  return { messageId, isrPrefix, routePath, lastModified, enqueuedAt };
}

export function report(context: LogContext, outcome: Outcome): void {
  console.log(JSON.stringify({ ...outcome, ...context }));
}
