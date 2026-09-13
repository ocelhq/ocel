import { db } from "@console/db";
import { connector } from "@console/db/schema";
import { and, eq } from "drizzle-orm";
import { changingSession } from "../changing";

export type OwnedConnector = { ok: true; connectorId: string } | { ok: false; refusal: Response };

export async function findOwnedConnector(request: Request, id: string): Promise<OwnedConnector> {
  const changing = await changingSession(request);
  if (!changing.ok) {
    return changing;
  }

  const [found] = await db
    .select({ id: connector.id })
    .from(connector)
    .where(
      and(
        eq(connector.id, id),
        eq(connector.organizationId, changing.session.activeOrganizationId),
      ),
    );

  if (!found) {
    return { ok: false, refusal: Response.json({ error: "Not found" }, { status: 404 }) };
  }
  return { ok: true, connectorId: found.id };
}
