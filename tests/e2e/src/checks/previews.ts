import { markerOrNone } from "../html";
import type { PreviewRelease } from "../targets/types";

const HOSTNAME_DEADLINE_MS = 5 * 60_000;
const HOSTNAME_POLL_MS = 10_000;
const SETTLE_MS = 5_000;
const PRUNE_DEADLINE_MS = 10 * 60_000;
const PRUNE_POLL_MS = 15_000;
const DEPLOYMENT_PATH = "/cache/deployment";
const DEPLOYMENT_MARKER = "deployment";

export type PreviewCheck = "own-release" | "pruned";

export const PREVIEW_NAME = "journey";

export const PREVIEW_TITLES: Record<PreviewCheck, string> = {
  "own-release": "preview · each deployment's own hostname serves its own release",
  pruned: "preview · a pruned deployment's own hostname stops serving",
};

export type Clock = {
  fetch: typeof fetch;
  sleep: (ms: number) => Promise<void>;
  now: () => number;
};

export const PREVIEW_CHECKS: PreviewCheck[] = ["own-release", "pruned"];

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

export type PreviewGateCredentials = { password: string; bypassSecret: string };

const GATE_LOGIN_PATH = "/.ocel/preview/login";
const GATE_BASIC_USER = "ocel";

async function requireGateRefusal(clock: Clock, url: string): Promise<void> {
  const answered = await clock.fetch(url, { redirect: "manual" });
  const body = await answered.text();
  if (answered.status !== 401 || body.includes("data-ocel=")) {
    throw new Error(
      `${url} answered ${answered.status} to a request without credentials, want 401 and no page`,
    );
  }
  if (answered.headers.get("cache-control") !== "private, no-store") {
    throw new Error(
      `${url} refused a request without credentials with cache-control "${answered.headers.get("cache-control")}", want "private, no-store"`,
    );
  }
}

async function requireGateServing(
  clock: Clock,
  url: string,
  id: string,
  how: string,
  init: RequestInit,
): Promise<void> {
  const answered = await clock.fetch(url, { ...init, redirect: "manual" });
  const body = await answered.text();
  const found = answered.status === 200 ? markerOrNone(body, DEPLOYMENT_MARKER) : undefined;
  if (found !== id) {
    throw new Error(
      `${url} answered ${answered.status} to a request with ${how}, want build ${id}`,
    );
  }
  if (answered.headers.get("x-robots-tag") !== "noindex") {
    throw new Error(`${url} served a request with ${how} without X-Robots-Tag: noindex`);
  }
}

export async function checkPreviewGate(
  clock: Clock,
  release: PreviewRelease,
  credentials: PreviewGateCredentials,
): Promise<void> {
  const url = `${release.deploymentUrl}${DEPLOYMENT_PATH}`;
  await requireGateRefusal(clock, url);
  await requireGateServing(clock, url, release.buildId, "Basic credentials", {
    headers: { authorization: `Basic ${btoa(`${GATE_BASIC_USER}:${credentials.password}`)}` },
  });
  await requireGateServing(clock, url, release.buildId, "the bypass header", {
    headers: { "x-ocel-preview-bypass": credentials.bypassSecret },
  });
  const login = await clock.fetch(`${release.deploymentUrl}${GATE_LOGIN_PATH}`, {
    method: "POST",
    headers: { "content-type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams({ password: credentials.password, next: DEPLOYMENT_PATH }),
    redirect: "manual",
  });
  const cookie = (login.headers.get("set-cookie") ?? "").split(";", 1)[0];
  if (login.status !== 303 || !cookie.startsWith("__Host-ocel-preview=")) {
    throw new Error(
      `${release.deploymentUrl}${GATE_LOGIN_PATH} answered ${login.status} to the password, want 303 with the session cookie`,
    );
  }
  await requireGateServing(clock, url, release.buildId, "the session cookie", {
    headers: { cookie },
  });
  await requireGateRefusal(clock, url);
}
