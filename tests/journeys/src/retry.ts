const ATTEMPTS = 5;
const FIRST_BACKOFF_MS = 1_000;
const LONGEST_BACKOFF_MS = 16_000;
const LONGEST_ASKED_WAIT_MS = 60_000;
const WAIT_BUDGET_MS = 120_000;

export type HttpIo = {
  api: typeof fetch;
  sleep: (ms: number) => Promise<void>;
  now: () => number;
  random: () => number;
};

export const LIVE_IO: HttpIo = { api: fetch, sleep: Bun.sleep, now: Date.now, random: Math.random };

function rateLimitSpent(answered: Response): boolean {
  return answered.headers.get("x-ratelimit-remaining") === "0";
}

function retryable(answered: Response): boolean {
  const throttled =
    answered.status === 429 ||
    (answered.status === 403 && (answered.headers.has("retry-after") || rateLimitSpent(answered)));
  return throttled || answered.status >= 500;
}

function askedWait(answered: Response, now: number): number | undefined {
  const retryAfter = answered.headers.get("retry-after");
  if (retryAfter !== null) {
    const seconds = Number(retryAfter);
    const until = Number.isFinite(seconds) ? now + seconds * 1_000 : Date.parse(retryAfter);
    return Number.isNaN(until) ? undefined : Math.max(0, until - now);
  }
  const reset = Number(answered.headers.get("x-ratelimit-reset") ?? Number.NaN);
  return rateLimitSpent(answered) && Number.isFinite(reset)
    ? Math.max(0, reset * 1_000 - now)
    : undefined;
}

function backoff(retry: number, random: () => number): number {
  const wait = Math.min(FIRST_BACKOFF_MS * 2 ** retry, LONGEST_BACKOFF_MS);
  return wait / 2 + random() * (wait / 2);
}

export async function sendWithRetry(io: HttpIo, url: string, init: RequestInit): Promise<Response> {
  let waited = 0;
  for (let retry = 0; ; retry++) {
    const answered = await io.api(url, init);
    if (!retryable(answered) || retry === ATTEMPTS - 1) {
      return answered;
    }
    const wait = askedWait(answered, io.now()) ?? backoff(retry, io.random);
    if (wait > LONGEST_ASKED_WAIT_MS || waited + wait > WAIT_BUDGET_MS) {
      return answered;
    }
    waited += wait;
    await io.sleep(wait);
  }
}
