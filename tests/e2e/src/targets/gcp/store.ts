import { HARNESS_PREFIX } from "../../identity";
import {
  APP_LABEL,
  ENVIRONMENT_LABEL,
  labelValue,
  NAMESPACE_LABEL,
  PRODUCTION_ENVIRONMENT,
  PROJECT_LABEL,
} from "./names";

export type Service = { name: string; uri: string; labels: Record<string, string> };

type Listing = {
  services?: Array<{ name?: string; uri?: string; labels?: Record<string, string> }>;
};

export function servicesIn(body: unknown): Service[] {
  return ((body as Listing).services ?? []).map((service) => ({
    name: (service.name ?? "").split("/").pop() ?? "",
    uri: service.uri ?? "",
    labels: service.labels ?? {},
  }));
}

export function findAppService(
  services: Service[],
  namespace: string,
  project: string,
  app: string,
): Service {
  const serving = servicesOf(services, namespace, project).filter(
    (service) =>
      service.labels[ENVIRONMENT_LABEL] === labelValue(PRODUCTION_ENVIRONMENT) &&
      service.labels[APP_LABEL] === labelValue(app),
  );
  const [only] = serving;
  if (!only) {
    const held = servicesOf(services, namespace, project).map((service) => service.name);
    throw new Error(
      `no Cloud Run service is labelled ${APP_LABEL}=${labelValue(app)} in project ${project}, so nothing ` +
        `says where ${app} is served (${held.join(", ") || "the project has none"})`,
    );
  }
  if (serving.length > 1) {
    throw new Error(
      `${serving.map((service) => service.name).join(" and ")} are all labelled ` +
        `${APP_LABEL}=${labelValue(app)} in project ${project}, so nothing says which one serves ${app}`,
    );
  }
  if (only.uri === "") {
    throw new Error(`${only.name} answers on no url of its own`);
  }
  return only;
}

export function exposedServices(body: unknown, namespace: string, project: string): string {
  const raw = (body as { services?: Array<{ labels?: Record<string, string> }> }).services ?? [];
  const held = raw.filter(
    (service) =>
      service.labels?.[NAMESPACE_LABEL] === labelValue(namespace) &&
      service.labels?.[PROJECT_LABEL] === labelValue(project),
  );
  return JSON.stringify(held);
}

function inNamespace(service: Service, namespace: string): boolean {
  return service.labels[NAMESPACE_LABEL] === labelValue(namespace);
}

export function servicesOf(services: Service[], namespace: string, project: string): Service[] {
  return services.filter(
    (service) =>
      inNamespace(service, namespace) && service.labels[PROJECT_LABEL] === labelValue(project),
  );
}

export function strayServices(services: Service[], namespace: string, mine: string[]): string[] {
  const projects = mine.map(labelValue);
  return services
    .filter((service) => {
      const project = service.labels[PROJECT_LABEL] ?? "";
      return (
        inNamespace(service, namespace) &&
        project.startsWith(HARNESS_PREFIX) &&
        !projects.includes(project)
      );
    })
    .map((service) => service.name);
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
  "iamcredentials.googleapis.com",
];

export const PREVIEW_APIS = ["iap.googleapis.com"];

export const TASKS_FEATURE = "tasks";

export const ALB_FEATURE = "alb-edge";

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
