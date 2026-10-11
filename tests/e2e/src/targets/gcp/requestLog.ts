import { bearer, type Where } from "./store";

const LOGGING_ENTRIES = "https://logging.googleapis.com/v2/entries:list";
const REQUEST_LOG = "run.googleapis.com/requests";
const PAGE_SIZE = 100;

export function originRequestFilter(urlFragment: string): string {
  const quoted = urlFragment.replaceAll("\\", "\\\\").replaceAll('"', '\\"');
  return [
    'resource.type="cloud_run_revision"',
    `log_id("${REQUEST_LOG}")`,
    `httpRequest.requestUrl:"${quoted}"`,
  ].join(" AND ");
}

export async function countOriginRequests(where: Where, urlFragment: string): Promise<number> {
  if (where.endpoint) {
    throw new Error(
      "Cloud Logging is not emulated, so the requests Cloud Run logged cannot be read",
    );
  }
  const answered = await fetch(LOGGING_ENTRIES, {
    method: "POST",
    headers: { "content-type": "application/json", ...bearer(where) },
    body: JSON.stringify({
      resourceNames: [`projects/${where.project}`],
      filter: originRequestFilter(urlFragment),
      pageSize: PAGE_SIZE,
    }),
  });
  if (!answered.ok) {
    throw new Error(`POST ${LOGGING_ENTRIES} = ${answered.status} ${await answered.text()}`);
  }
  const page = (await answered.json()) as { entries?: unknown[] };
  return page.entries?.length ?? 0;
}
