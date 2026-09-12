import { db } from "@console/db";
import { type Connector, connector } from "@console/db/schema";
import { and, eq } from "drizzle-orm";

export type { Connector };

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
