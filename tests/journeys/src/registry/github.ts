import type { Package } from "./packages";

export type PackageVersion = {
  id: number;
  created_at: string;
  metadata?: { container?: { tags?: string[] } };
};

export type Reclaim = { package: true } | { versions: number[] };

function tagged(version: PackageVersion): boolean {
  return (version.metadata?.container?.tags ?? []).length > 0;
}

export function reclaimed(versions: PackageVersion[], since: Date): Reclaim {
  const pushed = (version: PackageVersion) => Date.parse(version.created_at) >= since.getTime();
  const ours = versions.filter(pushed);
  const othersTagged = versions.some((version) => !pushed(version) && tagged(version));
  if (ours.some(tagged) && !othersTagged) {
    return { package: true };
  }
  return { versions: ours.map((version) => version.id) };
}

const API = "https://api.github.com";
const PAGE = 100;
const ATTEMPTS = 5;
const FIRST_WAIT_MS = 1_000;
const LONGEST_WAIT_MS = 16_000;

function again(status: number): boolean {
  return status === 429 || status >= 500;
}

function waitBefore(attempt: number, answered: Response): number {
  const asked = Number(answered.headers.get("retry-after"));
  const wait = asked > 0 ? asked * 1_000 : Math.min(FIRST_WAIT_MS * 2 ** attempt, LONGEST_WAIT_MS);
  return wait / 2 + Math.random() * (wait / 2);
}

async function call(api: typeof fetch, token: string, method: string, url: string) {
  for (let attempt = 0; ; attempt++) {
    const answered = await api(url, {
      method,
      headers: {
        accept: "application/vnd.github+json",
        authorization: `Bearer ${token}`,
        "x-github-api-version": "2022-11-28",
      },
    });
    if (!again(answered.status) || attempt === ATTEMPTS - 1) {
      return answered;
    }
    await Bun.sleep(waitBefore(attempt, answered));
  }
}

async function refused(method: string, url: string, answered: Response): Promise<Error> {
  return new Error(`${method} ${url} answered ${answered.status}: ${await answered.text()}`);
}

async function versionsOf(
  api: typeof fetch,
  token: string,
  at: string,
): Promise<PackageVersion[] | undefined> {
  const held: PackageVersion[] = [];
  for (let page = 1; ; page++) {
    const url = `${at}/versions?per_page=${PAGE}&page=${page}`;
    const answered = await call(api, token, "GET", url);
    if (answered.status === 404) {
      return undefined;
    }
    if (!answered.ok) {
      throw await refused("GET", url, answered);
    }
    const listed = (await answered.json()) as PackageVersion[];
    held.push(...listed);
    if (listed.length < PAGE) {
      return held;
    }
  }
}

async function remove(api: typeof fetch, token: string, url: string): Promise<void> {
  const answered = await call(api, token, "DELETE", url);
  if (!answered.ok) {
    throw await refused("DELETE", url, answered);
  }
}

export async function reclaimRegistry(
  packages: Package[],
  since: Date,
  token: string,
  api: typeof fetch = fetch,
): Promise<void> {
  for (const pkg of packages) {
    const at = `${API}/orgs/${pkg.org}/packages/container/${encodeURIComponent(pkg.name)}`;
    const versions = await versionsOf(api, token, at);
    if (versions === undefined) {
      continue;
    }
    const reclaim = reclaimed(versions, since);
    if ("package" in reclaim) {
      await remove(api, token, at);
      continue;
    }
    for (const id of reclaim.versions) {
      await remove(api, token, `${at}/versions/${id}`);
    }
  }
}
