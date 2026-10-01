/**
 * Thrown from a task's `run` or a topic consumer to fail the run or the message now, without
 * another attempt. Its message is recorded as the reason.
 */
export class AbortTaskRunError extends Error {
  override name = "AbortTaskRunError";
}
