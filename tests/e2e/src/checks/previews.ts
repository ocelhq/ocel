import { markerOrNone } from "../html";
import type { Cell } from "../matrix/types";
import type { PreviewRelease } from "../targets/types";

const HOSTNAME_DEADLINE_MS = 5 * 60_000;
const HOSTNAME_POLL_MS = 10_000;
const SETTLE_MS = 5_000;
const PRUNE_DEADLINE_MS = 10 * 60_000;
const PRUNE_POLL_MS = 15_000;
const DEPLOYMENT_PATH = "/cache/deployment";
const DEPLOYMENT_MARKER = "deployment";
const REFUSING_STATUSES = [302, 401, 403];

export type PreviewCheck = "own-release" | "pruned" | "refused";

export const PREVIEW_NAME = "journey";

export const PREVIEW_TITLES: Record<PreviewCheck, string> = {
  "own-release": "preview · each deployment's own hostname serves its own release",
  pruned: "preview · a pruned deployment's own hostname stops serving",
  refused:
    "preview · a deployment url behind Identity-Aware Proxy refuses an unauthenticated request",
};

export type Clock = {
  fetch: typeof fetch;
  sleep: (ms: number) => Promise<void>;
  now: () => number;
};

export function previewChecksFor(cell: Pick<Cell, "target" | "variant">): PreviewCheck[] {
  if (cell.target === "gcp" && cell.variant.config.edge === undefined) {
    return ["refused"];
  }
  return ["own-release", "pruned"];
}

type Seen = { served: boolean; said: string };

async function probeDeployment(clock: Clock, base: string, id: string): Promise<Seen> {
  const url = `${base}${DEPLOYMENT_PATH}`;
  try {
    const answered = await clock.fetch(url);
    const body = await answered.text();
    const found = answered.status === 200 ? markerOrNone(body, DEPLOYMENT_MARKER) : undefined;
    return {
      served: found === id,
      said:
        answered.status !== 200
          ? String(answered.status)
          : found === undefined
            ? "200 with no deployment marker"
            : `200 serving ${found}`,
    };
  } catch (error) {
    return {
      served: false,
      said: `fetch failed: ${error instanceof Error ? error.message : error}`,
    };
  }
}

async function pollUntil(
  clock: Clock,
  deadlineMs: number,
  pollMs: number,
  probe: () => Promise<boolean>,
): Promise<boolean> {
  const deadline = clock.now() + deadlineMs;
  for (;;) {
    if (await probe()) {
      return true;
    }
    if (clock.now() >= deadline) {
      return false;
    }
    await clock.sleep(pollMs);
  }
}

async function requireServing(clock: Clock, base: string, id: string): Promise<void> {
  let last = "nothing answered";
  const served = await pollUntil(clock, HOSTNAME_DEADLINE_MS, HOSTNAME_POLL_MS, async () => {
    const seen = await probeDeployment(clock, base, id);
    last = seen.said;
    return seen.served;
  });
  if (!served) {
    throw new Error(
      `${base}${DEPLOYMENT_PATH} never served build ${id} within ${HOSTNAME_DEADLINE_MS / 1000}s, and last answered ${last}`,
    );
  }
}

export async function checkOwnReleases(
  clock: Clock,
  app: string,
  first: PreviewRelease,
  second: PreviewRelease,
): Promise<void> {
  if (first.buildId === second.buildId) {
    throw new Error(`${app}'s two preview deployments both carry the build ${first.buildId}`);
  }
  for (const each of [first, second]) {
    await requireServing(clock, each.deploymentUrl, each.buildId);
    await clock.sleep(SETTLE_MS);
    const again = await probeDeployment(clock, each.deploymentUrl, each.buildId);
    if (!again.served) {
      throw new Error(
        `${each.deploymentUrl}${DEPLOYMENT_PATH} stopped serving build ${each.buildId} and answered ${again.said}`,
      );
    }
  }
  const [alias] = second.urls;
  if (alias === undefined) {
    throw new Error(`${app}'s newer preview deployment names no alias url`);
  }
  await requireServing(clock, alias, second.buildId);
}

export async function checkPruned(
  clock: Clock,
  app: string,
  pruned: PreviewRelease,
  kept: PreviewRelease,
): Promise<string> {
  let last = "";
  const stopped = await pollUntil(clock, PRUNE_DEADLINE_MS, PRUNE_POLL_MS, async () => {
    const seen = await probeDeployment(clock, pruned.deploymentUrl, pruned.buildId);
    last = seen.said;
    return !seen.served;
  });
  if (!stopped) {
    throw new Error(
      `${pruned.deploymentUrl}${DEPLOYMENT_PATH} of ${app} still serves build ${pruned.buildId} ${PRUNE_DEADLINE_MS / 1000}s after the prune`,
    );
  }
  const stillThere = await probeDeployment(clock, kept.deploymentUrl, kept.buildId);
  if (!stillThere.served) {
    throw new Error(
      `${kept.deploymentUrl}${DEPLOYMENT_PATH} of ${app} stopped serving the kept deployment's build ${kept.buildId} and answered ${stillThere.said}`,
    );
  }
  return last;
}

export async function checkRefusedWithoutIdentity(
  clock: Clock,
  releases: PreviewRelease[],
): Promise<Record<string, number>> {
  const seen: Record<string, number> = {};
  for (const each of releases) {
    for (const url of [...each.urls, each.deploymentUrl]) {
      const answered = await clock.fetch(url, { redirect: "manual" });
      const body = await answered.text();
      seen[url] = answered.status;
      if (!REFUSING_STATUSES.includes(answered.status) || body.includes("data-ocel=")) {
        throw new Error(
          `${url} answered ${answered.status} to a request with no identity, want one of ${REFUSING_STATUSES.join(", ")} and no page`,
        );
      }
    }
  }
  return seen;
}
