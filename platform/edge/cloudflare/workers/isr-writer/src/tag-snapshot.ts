import { entryMissHeader, tagSnapshotKey } from "@framework/next-cache";

const entityTag = /^(?:W\/)?("[^"]*")$/;

function opaqueTags(condition: string | null): string[] | "*" {
  const value = condition?.trim() ?? "";
  if (value === "*") return "*";
  return value.split(",").flatMap((part) => entityTag.exec(part.trim())?.[1] ?? []);
}

export async function readTagSnapshot(
  bucket: R2Bucket,
  isrPrefix: string,
  conditions: Headers,
): Promise<Response> {
  const key = tagSnapshotKey(isrPrefix);
  const held = opaqueTags(conditions.get("if-none-match"));
  if (held === "*" || held.length > 0) {
    const head = await bucket.head(key);
    if (
      head !== null &&
      (held === "*" || held.includes(entityTag.exec(head.httpEtag)?.[1] ?? ""))
    ) {
      return new Response(null, { status: 304, headers: { etag: head.httpEtag } });
    }
  }
  const object = await bucket.get(key);
  if (object === null) {
    return new Response("Not Found", { status: 404, headers: { [entryMissHeader]: "1" } });
  }
  return new Response(object.body, {
    headers: { "content-type": "application/json", etag: object.httpEtag },
  });
}
