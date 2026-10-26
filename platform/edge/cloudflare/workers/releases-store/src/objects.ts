import { objectKinds, parseObjectKey } from "@platform/edge-contract/storage-key";

const REMOVAL_BATCH = 1000;

function badRequest(message: string): Response {
  return new Response(message, { status: 400 });
}

function isProjectPrefix(prefix: string, project: string): boolean {
  const [kind, owner, ...rest] = prefix.split("/");
  return (
    (objectKinds as readonly string[]).includes(kind) &&
    owner === project &&
    rest.length > 0 &&
    !rest.includes("..")
  );
}

export async function putObjects(
  request: Request,
  bucket: R2Bucket,
  project: string,
): Promise<Response> {
  let form: FormData;
  try {
    form = await request.formData();
  } catch {
    return badRequest("the request carries no form of objects");
  }
  const objects: [string, File][] = [];
  for (const [key, value] of form.entries()) {
    if (typeof value === "string") return badRequest(`${key} carries no file`);
    const parsed = parseObjectKey(key);
    if (!parsed || parsed.rest === "" || parsed.project !== project) {
      return badRequest(`${key} lies outside a release of ${project}`);
    }
    objects.push([key, value]);
  }
  await Promise.all(
    objects.map(([key, file]) =>
      bucket.put(key, file.stream(), { httpMetadata: { contentType: file.type } }),
    ),
  );
  return new Response(null, { status: 204 });
}

export async function removeObjects(
  request: Request,
  bucket: R2Bucket,
  project: string,
): Promise<Response> {
  let body: { prefixes?: unknown };
  try {
    body = await request.json();
  } catch {
    return badRequest("the request names no prefixes");
  }
  const { prefixes } = body;
  if (
    !Array.isArray(prefixes) ||
    !prefixes.every((prefix) => typeof prefix === "string" && isProjectPrefix(prefix, project))
  ) {
    return badRequest(`a removal names prefixes under one of ${project}'s object kinds`);
  }
  for (const prefix of prefixes as string[]) {
    let cursor: string | undefined;
    do {
      const listed = await bucket.list({ prefix, cursor, limit: REMOVAL_BATCH });
      if (listed.objects.length > 0) await bucket.delete(listed.objects.map(({ key }) => key));
      cursor = listed.truncated ? listed.cursor : undefined;
    } while (cursor !== undefined);
  }
  return new Response(null, { status: 204 });
}
