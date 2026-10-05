import { HARNESS_PREFIX } from "../../identity";
import { bearer, type Where } from "./store";

const STORAGE = "https://storage.googleapis.com/storage/v1";

export type AppBucket = { name: string; project: string };

type Listing = {
  items?: Array<{ name?: string; labels?: Record<string, string> }>;
  nextPageToken?: string;
};

export function appBucketPrefix(namespace: string): string {
  return `${namespace}--`;
}

export function bucketsIn(body: unknown): AppBucket[] {
  return ((body as Listing).items ?? []).map((bucket) => ({
    name: bucket.name ?? "",
    project: bucket.labels?.["ocel-project"] ?? "",
  }));
}

export function strayBuckets(buckets: AppBucket[], mine: string[]): string[] {
  return buckets
    .filter((bucket) => bucket.project.startsWith(HARNESS_PREFIX) && !mine.includes(bucket.project))
    .map((bucket) => bucket.name);
}

async function answered(at: string, init: RequestInit): Promise<Response> {
  const response = await fetch(at, init);
  if (!response.ok && response.status !== 404) {
    throw new Error(`${init.method ?? "GET"} ${at} = ${response.status} ${await response.text()}`);
  }
  return response;
}

export async function listAppBuckets(where: Where, prefix: string): Promise<AppBucket[]> {
  const buckets: AppBucket[] = [];
  let pageToken = "";
  do {
    const at = new URL(`${STORAGE}/b`);
    at.searchParams.set("project", where.project);
    at.searchParams.set("prefix", prefix);
    if (pageToken) {
      at.searchParams.set("pageToken", pageToken);
    }
    const page = (await (
      await answered(at.toString(), { headers: bearer(where) })
    ).json()) as Listing;
    buckets.push(...bucketsIn(page));
    pageToken = page.nextPageToken ?? "";
  } while (pageToken);
  return buckets;
}

export async function deleteAppBucket(where: Where, name: string): Promise<void> {
  const bucket = `${STORAGE}/b/${encodeURIComponent(name)}`;
  let pageToken = "";
  do {
    const at = new URL(`${bucket}/o`);
    if (pageToken) {
      at.searchParams.set("pageToken", pageToken);
    }
    const listed = await answered(at.toString(), { headers: bearer(where) });
    if (listed.status === 404) {
      return;
    }
    const page = (await listed.json()) as Listing;
    for (const object of page.items ?? []) {
      await answered(`${bucket}/o/${encodeURIComponent(object.name ?? "")}`, {
        method: "DELETE",
        headers: bearer(where),
      });
    }
    pageToken = page.nextPageToken ?? "";
  } while (pageToken);
  await answered(bucket, { method: "DELETE", headers: bearer(where) });
}
