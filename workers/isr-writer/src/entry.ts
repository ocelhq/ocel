import { isRateLimited } from "./r2";

export type WriteOutcome = "written" | "rate-limited";

export async function writeEntry(
  bucket: R2Bucket,
  objectKey: string,
  body: string,
): Promise<WriteOutcome> {
  try {
    await bucket.put(objectKey, body, {
      httpMetadata: { contentType: "application/json" },
    });
    return "written";
  } catch (err) {
    if (isRateLimited(err)) return "rate-limited";
    throw err;
  }
}

export async function readEntry(
  bucket: R2Bucket,
  objectKey: string,
): Promise<string | null> {
  const object = await bucket.get(objectKey);
  return object === null ? null : object.text();
}
