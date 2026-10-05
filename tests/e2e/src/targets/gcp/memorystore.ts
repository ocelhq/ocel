import { HARNESS_PREFIX } from "../../identity";
import { sanitize } from "../../naming";
import { NAMESPACE_LABEL, PROJECT_LABEL } from "./names";
import { bearer, type Where } from "./store";

export const NETWORK_FEATURE = "private-network";

const MEMORYSTORE = "https://memorystore.googleapis.com";
const POLL_MS = 15_000;
const POLLS = 60;

export type CreateTime = {
  store: string;
  operation: string;
  createTime: string;
  endTime: string;
  seconds: number;
};

const CREATED = /^Created kv (\S+): its create operation (\S+) created (\S+) and ended (\S+)$/;

export function createTimesIn(output: string): CreateTime[] {
  return output.split("\n").flatMap((line) => {
    const matched = line.trim().match(CREATED);
    if (!matched) {
      return [];
    }
    const [, store = "", operation = "", createTime = "", endTime = ""] = matched;
    const seconds = (Date.parse(endTime) - Date.parse(createTime)) / 1000;
    return [{ store, operation, createTime, endTime, seconds }];
  });
}

export function storeFilter(namespace: string, slug: string): string {
  return `labels.${NAMESPACE_LABEL}="${sanitize(namespace)}" AND labels.${PROJECT_LABEL}="${sanitize(slug)}"`;
}

export type Store = { name: string; project: string };

type Listing = {
  instances?: Array<{ name?: string; labels?: Record<string, string> }>;
  nextPageToken?: string;
};

export function storesIn(body: unknown): Store[] {
  return ((body as Listing).instances ?? []).map((instance) => ({
    name: instance.name ?? "",
    project: instance.labels?.[PROJECT_LABEL] ?? "",
  }));
}

export function strayStores(stores: Store[], mine: string[]): string[] {
  return stores
    .filter((store) => store.project.startsWith(HARNESS_PREFIX) && !mine.includes(store.project))
    .map((store) => store.name);
}

async function answered(at: string, init: RequestInit): Promise<unknown> {
  const response = await fetch(at, init);
  if (!response.ok) {
    throw new Error(`${init.method ?? "GET"} ${at} = ${response.status} ${await response.text()}`);
  }
  return response.json();
}

export async function listStores(where: Where, filter: string): Promise<Store[]> {
  const stores: Store[] = [];
  let pageToken = "";
  do {
    const at = new URL(
      `${MEMORYSTORE}/v1/projects/${where.project}/locations/${where.region}/instances`,
    );
    at.searchParams.set("filter", filter);
    if (pageToken) {
      at.searchParams.set("pageToken", pageToken);
    }
    const page = (await answered(at.toString(), { headers: bearer(where) })) as Listing;
    stores.push(...storesIn(page));
    pageToken = page.nextPageToken ?? "";
  } while (pageToken);
  return stores;
}

type Operation = { name?: string; done?: boolean; error?: { message?: string } };

async function awaited(where: Where, doing: string, started: Operation): Promise<void> {
  let polled = started;
  for (let poll = 0; !polled.done; poll++) {
    if (poll >= POLLS) {
      throw new Error(`${doing} is still running after ${(POLLS * POLL_MS) / 1000}s`);
    }
    await new Promise((resolve) => setTimeout(resolve, POLL_MS));
    polled = (await answered(`${MEMORYSTORE}/v1/${polled.name}`, {
      headers: bearer(where),
    })) as Operation;
  }
  if (polled.error) {
    throw new Error(`${doing} failed: ${polled.error.message ?? "with no message"}`);
  }
}

export async function simulateMaintenance(where: Where, name: string): Promise<void> {
  const at = `${MEMORYSTORE}/v1/${name}?updateMask=simulate_maintenance_event`;
  const started = (await answered(at, {
    method: "PATCH",
    headers: { ...bearer(where), "content-type": "application/json" },
    body: JSON.stringify({ simulateMaintenanceEvent: true }),
  })) as Operation;
  await awaited(where, `a maintenance event on ${name}`, started);
}

export async function deleteStore(where: Where, name: string): Promise<void> {
  const at = `${MEMORYSTORE}/v1/${name}`;
  const response = await fetch(at, { method: "DELETE", headers: bearer(where) });
  if (response.status === 404) {
    return;
  }
  if (!response.ok) {
    throw new Error(`DELETE ${at} = ${response.status} ${await response.text()}`);
  }
  await awaited(where, `deleting ${name}`, (await response.json()) as Operation);
}
