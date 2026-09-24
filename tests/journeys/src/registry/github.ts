import type { Package } from "./packages";

const API = "https://api.github.com";
const ATTEMPTS = 5;
const FIRST_WAIT_MS = 1_000;
const LONGEST_WAIT_MS = 16_000;

function retryable(status: number): boolean {
  return status === 429 || status >= 500;
}

function waitBefore(attempt: number, answered: Response): number {
  const asked = Number(answered.headers.get("retry-after"));
  const wait = asked > 0 ? asked * 1_000 : Math.min(FIRST_WAIT_MS * 2 ** attempt, LONGEST_WAIT_MS);
  return wait / 2 + Math.random() * (wait / 2);
}

async function send(api: typeof fetch, token: string, method: string, url: string) {
  for (let attempt = 0; ; attempt++) {
    const answered = await api(url, {
      method,
      headers: {
        accept: "application/vnd.github+json",
        authorization: `Bearer ${token}`,
        "x-github-api-version": "2022-11-28",
      },
    });
    if (!retryable(answered.status) || attempt === ATTEMPTS - 1) {
      return answered;
    }
    await Bun.sleep(waitBefore(attempt, answered));
  }
}

async function refused(method: string, url: string, answered: Response): Promise<Error> {
  return new Error(`${method} ${url} answered ${answered.status}: ${await answered.text()}`);
}

async function held(api: typeof fetch, token: string, pkg: Package, url: string) {
  const answered = await send(api, token, "GET", url);
  if (answered.status === 404) {
    if (pkg.deployed) {
      throw new Error(
        `a registry cell deployed through ${pkg.org}/${pkg.name}, and ghcr holds no package of that name: the journey names the package apart from the CLI, so what the CLI pushed is never deleted`,
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
  api: typeof fetch = fetch,
): Promise<void> {
  for (const pkg of packages) {
    const url = `${API}/orgs/${pkg.org}/packages/container/${encodeURIComponent(pkg.name)}`;
    if (!(await held(api, token, pkg, url))) {
      continue;
    }
    const answered = await send(api, token, "DELETE", url);
    if (!answered.ok && answered.status !== 404) {
      throw await refused("DELETE", url, answered);
    }
  }
}
