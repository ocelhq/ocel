import type { RunContext } from "../worker/context.js";

/** How a run ended: with the output its `run` returned, or with the error that failed it. */
export type TaskResult<TOutput> = { ok: true; output: TOutput } | { ok: false; error: unknown };

/** What `catchError` answers to fail the run now instead of retrying it. */
export interface CatchErrorResult {
  /** Fails the run without another attempt. */
  skipRetrying?: boolean;
}

/** The hooks a task runs around its attempts. */
export interface TaskHooks<TPayload, TOutput> {
  /** Runs once the run succeeds. An error it throws is reported, and changes nothing. */
  onSuccess?: (args: { payload: TPayload; output: TOutput; ctx: RunContext }) => unknown;
  /** Runs once the run fails for good. An error it throws is reported, and changes nothing. */
  onFailure?: (args: { payload: TPayload; error: unknown; ctx: RunContext }) => unknown;
  /** Runs after `onSuccess` or `onFailure`. An error it throws is reported, and changes nothing. */
  onComplete?: (args: {
    payload: TPayload;
    ctx: RunContext;
    result: TaskResult<TOutput>;
  }) => unknown;
  /** Runs when the run is canceled during an attempt, best-effort. */
  onCancel?: (args: { payload: TPayload; ctx: RunContext }) => unknown;
  /** Sees every error an attempt throws; answering a {@link CatchErrorResult} of `{ skipRetrying: true }` fails the run now. */
  catchError?: (args: { payload: TPayload; error: unknown; ctx: RunContext }) => unknown;
  /** Wraps every attempt; `next` runs the attempt, and an error it throws fails the attempt. */
  middleware?: (args: { payload: TPayload; ctx: RunContext; next: () => Promise<void> }) => unknown;
  /** Runs before every attempt; an error it throws fails the attempt. */
  onStartAttempt?: (args: { payload: TPayload; ctx: RunContext }) => unknown;
}
