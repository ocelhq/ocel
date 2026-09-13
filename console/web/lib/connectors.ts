import { type ActiveOrganizationSession, roleOf } from "@console/auth";
import type { Connector as Dialled, Refusal } from "@console/connectors";
import { db } from "@console/db";
import { type Connector, connector } from "@console/db/schema";
import type { Ability } from "@ui/vars";
import { and, eq } from "drizzle-orm";
import { abilityOf, scopesFor } from "./connector-policy";
import { connectorToken } from "./connector-token";

export type { Connector };

const barred: Ability = { write: false, reveal: false };

export async function dial(
  session: ActiveOrganizationSession,
  held: Connector,
): Promise<Dialled | null> {
  if (held.url === null) {
    return null;
  }
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

export async function abilityFor(
  session: ActiveOrganizationSession,
  held: Connector | null,
): Promise<Ability> {
  if (held === null || held.url === null) {
    return barred;
  }
  const role = await roleOf(session.userId, session.activeOrganizationId);
  return abilityOf(scopesFor(role, held.capabilities));
}

export async function connectorsOf(organizationId: string): Promise<Connector[]> {
  return db.select().from(connector).where(eq(connector.organizationId, organizationId));
}

export async function connectorFor(
  organizationId: string,
  target: string | null,
): Promise<Connector | null> {
  if (target === null || target === "") {
    return null;
  }
  const [found] = await db
    .select()
    .from(connector)
    .where(and(eq(connector.organizationId, organizationId), eq(connector.target, target)));
  return found ?? null;
}

export async function noteDenial(
  connectorId: string,
  verb: string,
  refusal: Refusal,
): Promise<void> {
  if (refusal.reason !== "denied") {
    return;
  }
  await db
    .update(connector)
    .set({ lastDenied: { verb, at: new Date().toISOString(), message: refusal.message } })
    .where(eq(connector.id, connectorId));
}
