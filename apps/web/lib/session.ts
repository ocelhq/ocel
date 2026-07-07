import { db } from "@repo/db";
import { member } from "@repo/db/schema";
import { and, eq } from "drizzle-orm";
import { auth } from "@/lib/auth";

export type ActiveOrganizationSession = {
  userId: string;
  activeOrganizationId: string;
};

// `session.activeOrganizationId` has no FK constraint (see auth-schema.ts),
// so it can't be trusted at face value - membership is independently
// verified against the `member` table.
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

// Resolves the caller's Better Auth session from request headers. Returns
// null if there is no valid session at all.
export async function getSessionUserId(
  headers: Headers,
): Promise<string | null> {
  const session = await auth.api.getSession({ headers });
  return session?.user.id ?? null;
}

// Resolves the caller's session and its active organization, then
// independently confirms the caller is actually a member of that
// organization. Returns null if unauthenticated, if no organization is
// active on the session, or if membership doesn't verify.
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
