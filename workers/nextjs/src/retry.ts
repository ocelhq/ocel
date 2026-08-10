// Node middleware runs as a Lambda origin hop, so its availability is now a
// request-critical dependency: a throttle takes down every matched request.
// retryTransientOrigin absorbs a Lambda-service throttle or a thrown
// connection failure with a bounded, jittered budget — never a response the
// app produced on purpose, at any status, which is returned untouched.

// A throttle the Lambda service itself raised, distinct from a 429 the app's
// own middleware returned: Function URLs report a service-side rejection (a
// concurrency/rate limit, not the app's handler) with x-amzn-errortype set,
// the same AWS error-signaling convention the origin's own response headers
// already carry (see x-amzn-Remapped-content-length in cloud/aws/cmd/lambdanode).
// The app's response never sets this header.
//
// Unconfirmed in production: AWS documents 429 for a Function URL throttle,
// and x-amzn-ErrorType as a header a function sets for API Gateway error
// mapping, but nothing ties the two together for a Function URL's own
// synthesized throttle response. A false negative here is the safe failure —
// the 429 passes through untouched — because the alternative, matching on
// status alone, retries and discards an app-authored 429 (losing Retry-After)
// in favor of a 500. The narrow match also means an app that sets this header
// on its own 429 gets retried and swallowed (flattenHeaders in the bootstrap
// only strips Set-Cookie), a rarer cost than the one above.
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

// Exhausting the budget throws rather than returning the last throttle
// response: the caller's only fail-closed path is an error out of the invoker.
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
