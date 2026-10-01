import { type Duration, type DurationFields, encodeDuration } from "./duration.js";

/** How a failed attempt is retried. */
export interface RetryOptions {
  /** Attempts made before the run fails, the first included. */
  maxAttempts?: number;
  /** The backoff before the first retry. */
  minDelay?: Duration;
  /** The longest backoff between two attempts. */
  maxDelay?: Duration;
}

export interface RetryPolicyFields {
  maxAttempts: number;
  minDelay: DurationFields | undefined;
  maxDelay: DurationFields | undefined;
}

export function encodeRetryPolicy(retry: RetryOptions | undefined): RetryPolicyFields | undefined {
  if (retry === undefined) return undefined;
  return {
    maxAttempts: retry.maxAttempts ?? 0,
    minDelay: encodeDuration(retry.minDelay),
    maxDelay: encodeDuration(retry.maxDelay),
  };
}
