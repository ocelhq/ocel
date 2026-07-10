import { getSessionUserId, verifyOrganizationMembership } from "@repo/auth";
import { db } from "@repo/db";
import { uploadSession } from "@repo/db/schema";
import { eq } from "drizzle-orm";
import { aggregateState, type SessionFile } from "../session";

// GET /api/blob/status?sessionId=... . Backs
// runtime.v1.RuntimeService.GetUploadStatus, which drives the client's op=poll.
// Reads the shared store (the app DB) - never app-local memory - so status is
// correct regardless of which instance presigned the session, and aggregates
// the per-file states into one session-level state.
export async function uploadStatus(request: Request): Promise<Response> {
  const userId = await getSessionUserId(request.headers);
  if (!userId) {
    return Response.json({ error: "Unauthorized" }, { status: 401 });
  }

  const sessionId = new URL(request.url).searchParams.get("sessionId");
  if (!sessionId) {
    return Response.json({ error: "Missing sessionId" }, { status: 400 });
  }

  const [row] = await db
    .select()
    .from(uploadSession)
    .where(eq(uploadSession.id, sessionId));

  // Same 404 whether the session doesn't exist or the caller isn't a member of
  // its org - never leaking another tenant's session state to an authenticated
  // stranger (mirrors presign/detect).
  if (!row || !(await verifyOrganizationMembership(userId, row.organizationId))) {
    return Response.json({ error: "Not found" }, { status: 404 });
  }

  const state = aggregateState(row.files as SessionFile[]);
  // An expired session is a non-completion; surface a terminal error so the
  // client's poll loop fires its error callback (never onClientUploadComplete).
  const error = state === "expired" ? "upload expired" : undefined;
  return Response.json({ state, error }, { status: 200 });
}
