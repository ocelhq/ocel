import { HARNESS_PREFIX } from "../../identity";

export type Service = { name: string; uri: string };

type Listing = { services?: Array<{ name?: string; uri?: string }> };

export function servicesIn(body: unknown): Service[] {
  return ((body as Listing).services ?? []).map((service) => ({
    name: (service.name ?? "").split("/").pop() ?? "",
    uri: service.uri ?? "",
  }));
}

export function servedBy(services: Service[], names: string[]): string {
  const serving = names
    .map((name) => services.find((service) => service.name === name))
    .find((service) => service !== undefined);
  if (!serving) {
    throw new Error(
      `no Cloud Run service is named ${names.join(" or ")}, so nothing says where the app is served ` +
        `(${services.map((service) => service.name).join(", ") || "the project has none"})`,
    );
  }
  if (serving.uri === "") {
    throw new Error(`${serving.name} answers on no url of its own`);
  }
  return serving.uri;
}

export function exposedServices(body: unknown, names: string[]): string {
  const raw = (body as { services?: Array<{ name?: string }> }).services ?? [];
  const named = raw.filter((service) =>
    names.includes((service.name ?? "").split("/").pop() ?? ""),
  );
  return JSON.stringify(named);
}

export function hasAnyService(services: Service[], names: string[]): boolean {
  return services.some((service) => names.includes(service.name));
}

export function strayServices(names: string[], namespace: string, mine: string[]): string[] {
  const harness = `${namespace}-${HARNESS_PREFIX}`;
  return names.filter((name) => name.startsWith(harness) && !mine.includes(name));
}

export function reachable(uri: string, endpoint: string | undefined): string {
  if (!endpoint) {
    return uri;
  }
  const at = new URL(uri);
  at.host = `${at.hostname}:${new URL(endpoint).port}`;
  return at.origin;
}

export const BOOTSTRAP_APIS = [
  "firestore.googleapis.com",
  "storage.googleapis.com",
  "cloudkms.googleapis.com",
  "secretmanager.googleapis.com",
  "artifactregistry.googleapis.com",
  "iam.googleapis.com",
  "run.googleapis.com",
  "cloudscheduler.googleapis.com",
];

export const PREVIEW_APIS = ["iap.googleapis.com"];

export const TASKS_FEATURE = "tasks";

export const TASKS_APIS = ["pubsub.googleapis.com", "cloudtasks.googleapis.com"];

export async function switchOn(endpoint: string, project: string): Promise<void> {
  for (const api of [...BOOTSTRAP_APIS, ...PREVIEW_APIS, ...TASKS_APIS]) {
    const at = `${endpoint}/v1/projects/${project}/services/${api}:enable`;
    const answered = await fetch(at, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: "{}",
    });
    if (!answered.ok) {
      throw new Error(`POST ${at} = ${answered.status} ${await answered.text()}`);
    }
  }
}

export type Where = {
  endpoint: string | undefined;
  project: string;
  region: string;
  token: string | undefined;
};

function servicesUrl(where: Where): string {
  const host = where.endpoint ?? "https://run.googleapis.com";
  return `${host}/v2/projects/${where.project}/locations/${where.region}/services`;
}

export function bearer(where: Where): Record<string, string> {
  return where.token ? { authorization: `Bearer ${where.token}` } : {};
}

export async function readServices(where: Where): Promise<unknown> {
  const services: unknown[] = [];
  let pageToken = "";
  do {
    const at = new URL(servicesUrl(where));
    if (pageToken) {
      at.searchParams.set("pageToken", pageToken);
    }
    const answered = await fetch(at, { headers: bearer(where) });
    if (!answered.ok) {
      throw new Error(`GET ${at} = ${answered.status} ${await answered.text()}`);
    }
    const page = (await answered.json()) as { services?: unknown[]; nextPageToken?: string };
    services.push(...(page.services ?? []));
    pageToken = page.nextPageToken ?? "";
  } while (pageToken);
  return { services };
}

export async function listServices(where: Where): Promise<Service[]> {
  return servicesIn(await readServices(where));
}

export async function deleteService(where: Where, name: string): Promise<void> {
  const at = `${servicesUrl(where)}/${name}`;
  const answered = await fetch(at, { method: "DELETE", headers: bearer(where) });
  if (!answered.ok && answered.status !== 404) {
    throw new Error(`DELETE ${at} = ${answered.status} ${await answered.text()}`);
  }
}
