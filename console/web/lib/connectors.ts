import type { ActiveOrganizationSession } from "@console/auth";
import type { Connector as Dialled } from "@console/connectors";
import { db } from "@console/db";
import { type Connector, connector } from "@console/db/schema";
import { and, eq } from "drizzle-orm";
import { roleOf } from "./access";
import { scopesFor } from "./connector-policy";
import { connectorToken } from "./connector-token";

export type { Connector };

export async function dial(session: ActiveOrganizationSession, held: Connector): Promise<Dialled> {
  const role = await roleOf(session.userId, session.activeOrganizationId);
  return {
    id: held.id,
    url: held.url,
    capabilities: held.capabilities,
    token: await connectorToken({
      connectorId: held.id,
      organizationId: session.activeOrganizationId,
      userId: session.userId,
      scope: scopesFor(role, held.capabilities),
    }),
  };
}

export async function connectorsOf(organizationId: string): Promise<Connector[]> {
  return db.select().from(connector).where(eq(connector.organizationId, organizationId));
}

export async function connectorFor(
  organizationId: string,
  vendor: string | null,
): Promise<Connector | null> {
  if (vendor === null) {
    return null;
  }
  const [found] = await db
    .select()
    .from(connector)
    .where(
      and(
        eq(connector.organizationId, organizationId),
        eq(connector.vendor, vendor as Connector["vendor"]),
      ),
    );
  return found ?? null;
}

const onlineWithin = 90_000;

export type Liveness = "online" | "offline" | "never";

export function liveness(held: Pick<Connector, "connectedAt" | "lastSeenAt">): Liveness {
  if (held.connectedAt === null) {
    return "never";
  }
  if (held.lastSeenAt === null) {
    return "offline";
  }
  return Date.now() - held.lastSeenAt.getTime() < onlineWithin ? "online" : "offline";
}
