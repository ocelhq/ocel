import { HARNESS_PREFIX } from "../../identity";
import { sanitize } from "../../naming";
import { bearer, type Where } from "./store";

const SQL_ADMIN = "https://sqladmin.googleapis.com/v1";
const POLL_MS = 15_000;
const POLLS = 60;

export type Database = { name: string; project: string };

type Listing = {
  items?: Array<{ name?: string; settings?: { userLabels?: Record<string, string> } }>;
  nextPageToken?: string;
};

type Operation = {
  name?: string;
  status?: string;
  error?: { errors?: Array<{ message?: string }> };
};

export function databaseFilter(namespace: string): string {
  return `settings.userLabels.ocel-namespace:${sanitize(namespace)}`;
}

export function databasesIn(body: unknown): Database[] {
  return ((body as Listing).items ?? []).map((instance) => ({
    name: instance.name ?? "",
    project: instance.settings?.userLabels?.["ocel-project"] ?? "",
  }));
}

export function strayDatabases(databases: Database[], mine: string[]): string[] {
  return databases
    .filter(
      (database) => database.project.startsWith(HARNESS_PREFIX) && !mine.includes(database.project),
    )
    .map((database) => database.name);
}

async function answered(at: string, init: RequestInit): Promise<unknown> {
  const response = await fetch(at, init);
  if (!response.ok) {
    throw new Error(`${init.method ?? "GET"} ${at} = ${response.status} ${await response.text()}`);
  }
  return response.json();
}

export async function listDatabases(where: Where, filter: string): Promise<Database[]> {
  const databases: Database[] = [];
  let pageToken = "";
  do {
    const at = new URL(`${SQL_ADMIN}/projects/${where.project}/instances`);
    at.searchParams.set("filter", filter);
    if (pageToken) {
      at.searchParams.set("pageToken", pageToken);
    }
    const page = (await answered(at.toString(), { headers: bearer(where) })) as Listing;
    databases.push(...databasesIn(page));
    pageToken = page.nextPageToken ?? "";
  } while (pageToken);
  return databases;
}

export async function deleteDatabase(where: Where, name: string): Promise<void> {
  const at = `${SQL_ADMIN}/projects/${where.project}/instances/${name}`;
  const response = await fetch(at, { method: "DELETE", headers: bearer(where) });
  if (response.status === 404) {
    return;
  }
  if (!response.ok) {
    throw new Error(`DELETE ${at} = ${response.status} ${await response.text()}`);
  }
  let polled = (await response.json()) as Operation;
  for (let poll = 0; polled.status !== "DONE"; poll++) {
    if (poll >= POLLS) {
      throw new Error(`deleting ${name} is still running after ${(POLLS * POLL_MS) / 1000}s`);
    }
    await new Promise((resolve) => setTimeout(resolve, POLL_MS));
    polled = (await answered(`${SQL_ADMIN}/projects/${where.project}/operations/${polled.name}`, {
      headers: bearer(where),
    })) as Operation;
  }
  const failed = polled.error?.errors?.[0];
  if (failed) {
    throw new Error(`deleting ${name} failed: ${failed.message ?? "with no message"}`);
  }
}
