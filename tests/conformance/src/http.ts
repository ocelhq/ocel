import type { AnyUploader, Bucket } from "ocel/blob";
import { createUploadClient } from "ocel/blob/client";

export async function poll<T>(
  fn: () => Promise<T | undefined>,
  { timeoutMs = 30_000, intervalMs = 500 } = {},
): Promise<T | undefined> {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    const value = await fn();
    if (value !== undefined) return value;
    if (Date.now() >= deadline) return undefined;
    await new Promise((resolve) => setTimeout(resolve, intervalMs));
  }
}

export type Document = {
  id: number;
  key: string;
  name: string;
  mime_type: string;
  size: string;
  owner_id: string | null;
  thumbnail_key: string | null;
};

export async function uploadDocument(baseUrl: string, ownerId: string) {
  const client = createUploadClient<Bucket<Record<string, AnyUploader>>>({
    url: `${baseUrl}/api/upload`,
    pollIntervalMs: 250,
    maxPollMs: 30_000,
  });
  const image = Buffer.from(
    "iVBORw0KGgoAAAANSUhEUgAAABAAAAAQCAIAAACQkWg2AAAAUElEQVR42pXLSQ0AIAwF0UpBCtIqDSlIgQTC2uU3meMbqplDUUgnLhTSgWFodFgaGk7tD492hl9bg6jVQdPyYGhhsPU7uPoaEL0HUM8B131oYrCCEFU/lXsAAAAASUVORK5CYII=",
    "base64",
  );
  const result = await client.upload("document", {
    files: [new File([image], "photo.png", { type: "image/png" })],
    input: { ownerId },
  });
  const key = result.files[0]?.key;
  if (!key) throw new Error("the upload returned no file key");
  return { image, key };
}

export async function findDocument(
  baseUrl: string,
  key: string,
  ready: (document: Document) => boolean = () => true,
) {
  return poll(async () => {
    const response = await fetch(`${baseUrl}/api/documents`);
    if (!response.ok) return undefined;
    const documents = (await response.json()) as Document[];
    const document = documents.find((candidate) => candidate.key === key);
    return document && ready(document) ? document : undefined;
  });
}
