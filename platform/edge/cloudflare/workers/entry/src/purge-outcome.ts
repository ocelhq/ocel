import { type PurgeOutcome, RATE_LIMITED } from "./purge-backlog";

const RATE_LIMITED_IN_MESSAGE = new RegExp(`\\b${RATE_LIMITED}\\b`);

export function outcomeOfPurge(answer: unknown): PurgeOutcome {
  if (answer instanceof Error) {
    return {
      ok: false,
      codes: RATE_LIMITED_IN_MESSAGE.test(answer.message) ? [RATE_LIMITED] : [],
      message: answer.message,
    };
  }
  const result = answer as CachePurgeResult;
  if (result.success) return { ok: true };
  return {
    ok: false,
    codes: result.errors.map((error) => error.code),
    message: result.errors.map((error) => `${error.code}: ${error.message}`).join("; "),
  };
}
