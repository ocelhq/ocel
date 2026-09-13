import {
  type ActiveOrganizationSession,
  administers,
  getActiveOrganizationSession,
  roleOf,
} from "@console/auth";

export type Changing =
  | { ok: true; session: ActiveOrganizationSession }
  | { ok: false; refusal: Response };

export async function changingSession(request: Request): Promise<Changing> {
  const session = await getActiveOrganizationSession(request.headers);
  if (!session) {
    return { ok: false, refusal: Response.json({ error: "Unauthorized" }, { status: 401 }) };
  }

  if (!administers(await roleOf(session.userId, session.activeOrganizationId))) {
    return {
      ok: false,
      refusal: Response.json(
        { error: "Only an owner or an admin may change a connector" },
        { status: 403 },
      ),
    };
  }

  return { ok: true, session };
}
