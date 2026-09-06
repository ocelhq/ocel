import { getActiveOrganizationSession } from "@console/auth";
import { db } from "@console/db";
import { project } from "@console/db/schema";
import { and, eq } from "drizzle-orm";

export type OwnedProject = { ok: true; projectId: string } | { ok: false; refusal: Response };

export async function findOwnedProject(request: Request, id: string): Promise<OwnedProject> {
  const session = await getActiveOrganizationSession(request.headers);
  if (!session) {
    return { ok: false, refusal: Response.json({ error: "Unauthorized" }, { status: 401 }) };
  }

  const [found] = await db
    .select({ id: project.id })
    .from(project)
    .where(and(eq(project.id, id), eq(project.organizationId, session.activeOrganizationId)));

  if (!found) {
    return { ok: false, refusal: Response.json({ error: "Not found" }, { status: 404 }) };
  }
  return { ok: true, projectId: found.id };
}
