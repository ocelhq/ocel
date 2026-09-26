import type { Package } from "./packages";

const API = "https://api.github.com";
const ATTEMPTS = 5;
const FIRST_BACKOFF_MS = 1_000;
const LONGEST_BACKOFF_MS = 16_000;
const LONGEST_ASKED_WAIT_MS = 60_000;
const WAIT_BUDGET_MS = 120_000;

export type GitHubIo = {
  api: typeof fetch;
  sleep: (ms: number) => Promise<void>;
  now: () => number;
  random: () => number;
};

const LIVE: GitHubIo = { api: fetch, sleep: Bun.sleep, now: Date.now, random: Math.random };

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

async function send(io: GitHubIo, token: string, method: string, url: string) {
  let waited = 0;
  for (let retry = 0; ; retry++) {
    const answered = await io.api(url, {
      method,
      headers: {
        accept: "application/vnd.github+json",
        authorization: `Bearer ${token}`,
        "x-github-api-version": "2022-11-28",
      },
    });
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

async function refused(method: string, url: string, answered: Response): Promise<Error> {
  return new Error(`${method} ${url} answered ${answered.status}: ${await answered.text()}`);
}

async function packageExists(io: GitHubIo, token: string, pkg: Package, url: string) {
  const answered = await send(io, token, "GET", url);
  if (answered.status === 404) {
    if (pkg.deployed) {
      throw new Error(
        `a registry cell deployed through ${pkg.org}/${pkg.name}, and ghcr has no package of that name: the journey names the package apart from the CLI, so what the CLI pushed is never deleted`,
      );
    }
    return false;
  }
  if (!answered.ok) {
    throw await refused("GET", url, answered);
  }
  return true;
}

export async function deletePackages(
  packages: Package[],
  token: string,
  io: GitHubIo = LIVE,
): Promise<void> {
  for (const pkg of packages) {
    const url = `${API}/orgs/${pkg.org}/packages/container/${encodeURIComponent(pkg.name)}`;
    if (!(await packageExists(io, token, pkg, url))) {
      continue;
    }
    const answered = await send(io, token, "DELETE", url);
    if (!answered.ok && answered.status !== 404) {
      throw await refused("DELETE", url, answered);
    }
  }
}
