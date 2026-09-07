import { HARNESS_PREFIX } from "../../identity";

export type Standing = "live" | "done" | "unknown";

export type Verdict = { standing: Standing; reason?: string };

export type LookRun = (id: string) => Promise<Verdict>;

export type Unreadable = { id: string; reason: string };

export type Lively = { keep: Set<string>; unreadable: Unreadable[] };

const RUN_ID = new RegExp(`^${HARNESS_PREFIX}(\\d+)-`);

const LIVE = new Set(["queued", "in_progress", "waiting", "requested", "pending"]);

export function runIdOf(name: string): string | undefined {
  return RUN_ID.exec(name)?.[1];
}

export async function livelyRuns(ids: Iterable<string>, look: LookRun): Promise<Lively> {
  const keep = new Set<string>();
  const unreadable: Unreadable[] = [];
  for (const id of new Set(ids)) {
    const verdict = await look(id);
    if (verdict.standing === "done") {
      continue;
    }
    keep.add(id);
    if (verdict.standing === "unknown") {
      unreadable.push({ id, reason: verdict.reason ?? "the run gave no reason" });
    }
  }
  return { keep, unreadable };
}

async function asked(
  fetching: typeof fetch,
  repository: string,
  token: string,
  id: string,
): Promise<Verdict> {
  let answer: Response;
  try {
    answer = await fetching(`https://api.github.com/repos/${repository}/actions/runs/${id}`, {
      headers: {
        Accept: "application/vnd.github+json",
        Authorization: `Bearer ${token}`,
        "X-GitHub-Api-Version": "2022-11-28",
      },
    });
  } catch (error) {
    return { standing: "unknown", reason: String(error) };
  }
  if (answer.status === 404) {
    return { standing: "done" };
  }
  if (!answer.ok) {
    return { standing: "unknown", reason: `github answered ${answer.status}` };
  }
  let status: unknown;
  try {
    status = ((await answer.json()) as { status?: unknown }).status;
  } catch (error) {
    return { standing: "unknown", reason: String(error) };
  }
  if (typeof status !== "string") {
    return { standing: "unknown", reason: "the run carries no status" };
  }
  if (LIVE.has(status)) {
    return { standing: "live" };
  }
  if (status === "completed") {
    return { standing: "done" };
  }
  return { standing: "unknown", reason: `the run reads ${status}` };
}

export function githubRuns(env: NodeJS.ProcessEnv, fetching: typeof fetch = fetch): LookRun {
  const repository = env.GITHUB_REPOSITORY?.trim();
  const token = env.GITHUB_TOKEN?.trim() || env.GH_TOKEN?.trim();
  const seen = new Map<string, Promise<Verdict>>();
  return (id) => {
    if (!repository || !token) {
      return Promise.resolve({
        standing: "unknown",
        reason: "the environment names no repository and no token to read a run with",
      });
    }
    let standing = seen.get(id);
    if (!standing) {
      standing = asked(fetching, repository, token, id);
      seen.set(id, standing);
    }
    return standing;
  };
}

export function ofRun(names: Iterable<string>, runId: string): string[] {
  const mine = new Set<string>();
  for (const name of names) {
    if (runIdOf(name) === runId) {
      mine.add(name);
    }
  }
  return [...mine];
}
