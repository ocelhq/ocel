import { db } from "@console/db";
import { projectEnvValue } from "@console/db/schema";
import { eq } from "drizzle-orm";
import { findOwnedProject } from "./owned";

export async function listProjectEnv(request: Request, id: string): Promise<Response> {
  const owned = await findOwnedProject(request, id);
  if (!owned.ok) {
    return owned.refusal;
  }

  const values = await db
    .select({
      key: projectEnvValue.key,
      value: projectEnvValue.value,
      updatedAt: projectEnvValue.updatedAt,
    })
    .from(projectEnvValue)
    .where(eq(projectEnvValue.projectId, owned.projectId))
    .orderBy(projectEnvValue.key);

  return Response.json(
    values.map((row) => ({ key: row.key, value: row.value, updatedAt: row.updatedAt.getTime() })),
    { status: 200 },
  );
}
