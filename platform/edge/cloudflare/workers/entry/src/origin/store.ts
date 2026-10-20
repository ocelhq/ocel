import type { OriginStore } from "@platform/edge-contract/origin-reach";
import { AwsClient } from "aws4fetch";
import { encodeKeyPath } from "../serve-key";
import type { StoreSource } from "../store-read";

export interface OriginObjects {
  readObject(key: string): Promise<Response>;
  objectUrl(key: string): string;
}

function openObjectsUnder(client: AwsClient, url: string, prefix: string): OriginObjects {
  const objectUrl = (key: string) => `${url}/${encodeKeyPath(prefix + key)}`;
  return { objectUrl, readObject: (key) => client.fetch(objectUrl(key)) };
}

function openEntrySource(objects: OriginObjects): StoreSource {
  return {
    async get(key) {
      const res = await objects.readObject(key);
      if (res.ok) return { text: () => res.text() };
      await res.body?.cancel();
      if (res.status === 404 || res.status === 403) return null;
      throw new Error(
        `ocel: the origin's store answered ${res.status} for ${objects.objectUrl(key)}`,
      );
    },
  };
}

export interface OpenedStore {
  objects: OriginObjects;
  entries?: StoreSource;
}

export function openStore(store: OriginStore): OpenedStore {
  const { region, accessKeyId, secretAccessKey } = store;
  const client = new AwsClient({ accessKeyId, secretAccessKey, service: "s3", region, retries: 0 });
  const url = store.url.replace(/\/$/, "");
  return {
    objects: openObjectsUnder(client, url, ""),
    entries: store.holdsEntries
      ? openEntrySource(openObjectsUnder(client, url, store.entriesPrefix ?? ""))
      : undefined,
  };
}
