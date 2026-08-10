export function isServiceThrottle(response: Response): boolean {
  return response.status === 429 && response.headers.has("x-amzn-errortype");
}

interface RetrySeam {
  sleep: (ms: number) => Promise<void>;
  random: () => number;
}

const DEFAULT_SEAM: RetrySeam = {
  sleep: (ms) => new Promise((resolve) => setTimeout(resolve, ms)),
  random: Math.random,
};

const ATTEMPTS = 3;
const BASE_DELAY_MS = 50;

export async function retryTransientOrigin(
  attempt: () => Promise<Response>,
  seam: Partial<RetrySeam> = {},
): Promise<Response> {
  const { sleep, random } = { ...DEFAULT_SEAM, ...seam };
  let lastError: unknown;
  for (let i = 0; i < ATTEMPTS; i++) {
    try {
      const response = await attempt();
      if (!isServiceThrottle(response)) return response;
      await response.body?.cancel();
      lastError = new Error(
        `middleware origin throttled (429) on attempt ${i + 1}/${ATTEMPTS}`,
      );
    } catch (error) {
      lastError = error;
    }
    if (i < ATTEMPTS - 1) {
      await sleep(BASE_DELAY_MS * 2 ** i * (1 + random()));
    }
  }
  throw lastError;
}
