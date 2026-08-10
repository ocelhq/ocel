import { getSessionUserId, verifyOrganizationMembership } from "@repo/auth";
import { db } from "@repo/db";
import { project } from "@repo/db/schema";
import { eq } from "drizzle-orm";

export async function getProjectById(
  request: Request,
  id: string,
): Promise<Response> {
  const userId = await getSessionUserId(request.headers);
  if (!userId) {
    return Response.json({ error: "Unauthorized" }, { status: 401 });
  }

  const [found] = await db.select().from(project).where(eq(project.id, id));

  if (!found) {
    return Response.json({ error: "Not found" }, { status: 404 });
  }

  const isMember = await verifyOrganizationMembership(
    userId,
    found.organizationId,
  );
  if (!isMember) {
    return Response.json({ error: "Not found" }, { status: 404 });
  }

  return Response.json(found, { status: 200 });
}
