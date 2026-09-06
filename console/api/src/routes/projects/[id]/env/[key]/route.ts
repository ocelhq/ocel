import { db } from "@console/db";
import { projectEnvValue } from "@console/db/schema";
import { and, eq } from "drizzle-orm";
import { uuidv7 } from "uuidv7";
import { findOwnedProject } from "../owned";
import { envKeySchema, putEnvValueSchema } from "../validation";

type Addressed = { ok: true; projectId: string; key: string } | { ok: false; refusal: Response };

async function addressedValue(request: Request, id: string, key: string): Promise<Addressed> {
  const owned = await findOwnedProject(request, id);
  if (!owned.ok) {
    return owned;
  }

  const named = envKeySchema.safeParse(key);
  if (!named.success) {
    return {
      ok: false,
      refusal: Response.json({ error: "Invalid variable name" }, { status: 400 }),
    };
  }
  return { ok: true, projectId: owned.projectId, key: named.data };
}

export async function getProjectEnvValue(
  request: Request,
  id: string,
  key: string,
): Promise<Response> {
  const addressed = await addressedValue(request, id, key);
  if (!addressed.ok) {
    return addressed.refusal;
  }

  const [held] = await db
    .select({
      key: projectEnvValue.key,
      value: projectEnvValue.value,
      updatedAt: projectEnvValue.updatedAt,
    })
    .from(projectEnvValue)
    .where(
      and(
        eq(projectEnvValue.projectId, addressed.projectId),
        eq(projectEnvValue.key, addressed.key),
      ),
    );

  if (!held) {
    return Response.json({ error: "Not found" }, { status: 404 });
  }
  return Response.json(
    { key: held.key, value: held.value, updatedAt: held.updatedAt.getTime() },
    { status: 200 },
  );
}

export async function putProjectEnvValue(
  request: Request,
  id: string,
  key: string,
): Promise<Response> {
  const addressed = await addressedValue(request, id, key);
  if (!addressed.ok) {
    return addressed.refusal;
  }

  let body: unknown;
  try {
    body = await request.json();
  } catch {
    return Response.json({ error: "Invalid request body" }, { status: 400 });
  }
  const parsed = putEnvValueSchema.safeParse(body);
  if (!parsed.success) {
    return Response.json({ error: "Invalid request" }, { status: 400 });
  }

  const [stored] = await db
    .insert(projectEnvValue)
    .values({
      id: uuidv7(),
      projectId: addressed.projectId,
      key: addressed.key,
      value: parsed.data.value,
    })
    .onConflictDoUpdate({
      target: [projectEnvValue.projectId, projectEnvValue.key],
      set: { value: parsed.data.value, updatedAt: new Date() },
    })
    .returning();

  return Response.json(
    { key: stored.key, size: stored.value.length, updatedAt: stored.updatedAt.getTime() },
    { status: 200 },
  );
}

export async function deleteProjectEnvValue(
  request: Request,
  id: string,
  key: string,
): Promise<Response> {
  const addressed = await addressedValue(request, id, key);
  if (!addressed.ok) {
    return addressed.refusal;
  }

  const removed = await db
    .delete(projectEnvValue)
    .where(
      and(
        eq(projectEnvValue.projectId, addressed.projectId),
        eq(projectEnvValue.key, addressed.key),
      ),
    )
    .returning({ key: projectEnvValue.key });

  return Response.json({ deleted: removed.length > 0 }, { status: 200 });
}
