import { getActiveOrganizationSession } from "@console/auth";
import { db } from "@console/db";
import { connector } from "@console/db/schema";
import { and, eq } from "drizzle-orm";

export type OwnedConnector = { ok: true; connectorId: string } | { ok: false; refusal: Response };

export async function findOwnedConnector(request: Request, id: string): Promise<OwnedConnector> {
  const session = await getActiveOrganizationSession(request.headers);
  if (!session) {
    return { ok: false, refusal: Response.json({ error: "Unauthorized" }, { status: 401 }) };
  }

  const [found] = await db
    .select({ id: connector.id })
    .from(connector)
    .where(and(eq(connector.id, id), eq(connector.organizationId, session.activeOrganizationId)));

  if (!found) {
    return { ok: false, refusal: Response.json({ error: "Not found" }, { status: 404 }) };
  }
  return { ok: true, connectorId: found.id };
}
