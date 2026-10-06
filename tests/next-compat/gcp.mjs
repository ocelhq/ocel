import { execFileSync } from "node:child_process";

export const NAMESPACE_LABEL = "ocel-namespace";

export const PROJECT_LABEL = "ocel-project";

const TOKEN_TIMEOUT_MS = 30_000;

export function readAccessToken() {
  return execFileSync("gcloud", ["auth", "print-access-token"], {
    encoding: "utf8",
    timeout: TOKEN_TIMEOUT_MS,
  }).trim();
}

export async function listProjectSlugs({
  project,
  region,
  namespace,
  token,
  fetch = globalThis.fetch,
}) {
  const base = `https://run.googleapis.com/v2/projects/${project}/locations/${region}/services`;
  const slugs = new Set();
  let pageToken = "";
  do {
    const url = pageToken ? `${base}?${new URLSearchParams({ pageToken })}` : base;
    const answered = await fetch(url, { headers: { authorization: `Bearer ${token}` } });
    if (!answered.ok) {
      throw new Error(`GET ${url} = ${answered.status} ${await answered.text()}`);
    }
    const page = await answered.json();
    for (const service of page.services ?? []) {
      const labels = service.labels ?? {};
      if (labels[NAMESPACE_LABEL] === namespace && labels[PROJECT_LABEL]) {
        slugs.add(labels[PROJECT_LABEL]);
      }
    }
    pageToken = page.nextPageToken ?? "";
  } while (pageToken);
  return [...slugs].sort();
}
