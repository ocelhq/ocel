import { type HttpIo, LIVE_IO, sendWithRetry } from "../retry";
import type { Package } from "./packages";

const API = "https://api.github.com";

function send(io: HttpIo, token: string, method: string, url: string): Promise<Response> {
  return sendWithRetry(io, url, {
    method,
    headers: {
      accept: "application/vnd.github+json",
      authorization: `Bearer ${token}`,
      "x-github-api-version": "2022-11-28",
    },
  });
}

async function refused(method: string, url: string, answered: Response): Promise<Error> {
  return new Error(`${method} ${url} answered ${answered.status}: ${await answered.text()}`);
}

async function packageExists(io: HttpIo, token: string, pkg: Package, url: string) {
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
  io: HttpIo = LIVE_IO,
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
