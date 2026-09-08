import { HARNESS_PREFIX } from "../../identity";

export type Service = { name: string; uri: string };

type Listing = { services?: Array<{ name?: string; uri?: string }> };

const HASHED = /^-[0-9a-f]{6}$/;

export function servicesIn(body: unknown): Service[] {
  return ((body as Listing).services ?? []).map((service) => ({
    name: (service.name ?? "").split("/").pop() ?? "",
    uri: service.uri ?? "",
  }));
}

export function under(services: Service[], lead: string): Service[] {
  return services.filter((service) => service.name.startsWith(`${lead}-`));
}

export function servedBy(services: Service[], lead: string): string {
  const named = under(services, lead);
  const own = named.filter((service) => HASHED.test(service.name.slice(lead.length)));
  const [serving] = own.length > 0 ? own : named;
  if (!serving) {
    throw new Error(
      `no Cloud Run service is named ${lead}-*, so nothing says where the app is served ` +
        `(${services.map((service) => service.name).join(", ") || "the project holds none"})`,
    );
  }
  if (serving.uri === "") {
    throw new Error(`${serving.name} answers on no url of its own`);
  }
  return serving.uri;
}

export function standing(services: Service[], leads: string[]): boolean {
  return leads.some((lead) => under(services, lead).length > 0);
}

export function strayServices(names: string[], namespace: string, mine: string[]): string[] {
  const harness = `${namespace}-${HARNESS_PREFIX}`;
  return names.filter(
    (name) =>
      name.startsWith(harness) &&
      !mine.some((lead) => name === lead || name.startsWith(`${lead}-`)),
  );
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
];

export async function switchOn(endpoint: string, project: string): Promise<void> {
  for (const api of BOOTSTRAP_APIS) {
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

function bearer(where: Where): Record<string, string> {
  return where.token ? { authorization: `Bearer ${where.token}` } : {};
}

export async function listServices(where: Where): Promise<Service[]> {
  const at = servicesUrl(where);
  const answered = await fetch(at, { headers: bearer(where) });
  if (!answered.ok) {
    throw new Error(`GET ${at} = ${answered.status} ${await answered.text()}`);
  }
  return servicesIn(await answered.json());
}

export async function deleteService(where: Where, name: string): Promise<void> {
  const at = `${servicesUrl(where)}/${name}`;
  const answered = await fetch(at, { method: "DELETE", headers: bearer(where) });
  if (!answered.ok && answered.status !== 404) {
    throw new Error(`DELETE ${at} = ${answered.status} ${await answered.text()}`);
  }
}
