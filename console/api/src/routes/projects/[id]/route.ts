import {
  getActiveOrganizationSession,
  getSessionUserId,
  verifyOrganizationMembership,
} from "@console/auth";
import { db } from "@console/db";
import { project } from "@console/db/schema";
import { and, eq } from "drizzle-orm";

export async function getProjectById(request: Request, id: string): Promise<Response> {
  const userId = await getSessionUserId(request.headers);
  if (!userId) {
    return Response.json({ error: "Unauthorized" }, { status: 401 });
  }

  const [found] = await db.select().from(project).where(eq(project.id, id));

  if (!found) {
    return Response.json({ error: "Not found" }, { status: 404 });
  }

  const isMember = await verifyOrganizationMembership(userId, found.organizationId);
  if (!isMember) {
    return Response.json({ error: "Not found" }, { status: 404 });
  }

  return Response.json(found, { status: 200 });
}

export async function deleteProject(request: Request, id: string): Promise<Response> {
  const session = await getActiveOrganizationSession(request.headers);
  if (!session) {
    return Response.json({ error: "Unauthorized" }, { status: 401 });
  }

  const [deleted] = await db
    .delete(project)
    .where(and(eq(project.id, id), eq(project.organizationId, session.activeOrganizationId)))
    .returning({ id: project.id });

  if (!deleted) {
    return Response.json({ error: "Not found" }, { status: 404 });
  }

  return new Response(null, { status: 204 });
}
