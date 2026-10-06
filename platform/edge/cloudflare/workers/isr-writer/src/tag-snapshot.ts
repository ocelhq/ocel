import { entryMissHeader, tagSnapshotKey } from "@framework/next-cache";

export async function readTagSnapshot(
  bucket: R2Bucket,
  isrPrefix: string,
  conditions: Headers,
): Promise<Response> {
  const object = await bucket.get(tagSnapshotKey(isrPrefix), { onlyIf: conditions });
  if (object === null) {
    return new Response("Not Found", { status: 404, headers: { [entryMissHeader]: "1" } });
  }
  if (!("body" in object)) {
    return new Response(null, { status: 304, headers: { etag: object.httpEtag } });
  }
  return new Response(object.body, {
    headers: { "content-type": "application/json", etag: object.httpEtag },
  });
}
