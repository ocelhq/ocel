import { db } from "@console/db";
import { member } from "@console/db/schema";
import { and, eq } from "drizzle-orm";
import { auth } from "./auth";

export type ActiveOrganizationSession = {
  userId: string;
  activeOrganizationId: string;
};

export async function verifyOrganizationMembership(
  userId: string,
  organizationId: string,
): Promise<boolean> {
  const rows = await db
    .select({ id: member.id })
    .from(member)
    .where(
      and(eq(member.userId, userId), eq(member.organizationId, organizationId)),
    )
    .limit(1);

  return rows.length > 0;
}

export async function getSessionUserId(
  headers: Headers,
): Promise<string | null> {
  const session = await auth.api.getSession({ headers });
  return session?.user.id ?? null;
}

export async function getActiveOrganizationSession(
  headers: Headers,
): Promise<ActiveOrganizationSession | null> {
  const session = await auth.api.getSession({ headers });
  if (!session) {
    return null;
  }

  const activeOrganizationId = session.session.activeOrganizationId;
  if (!activeOrganizationId) {
    return null;
  }

  const isMember = await verifyOrganizationMembership(
    session.user.id,
    activeOrganizationId,
  );
  if (!isMember) {
    return null;
  }

  return { userId: session.user.id, activeOrganizationId };
}
