import { getSessionUserId, verifyOrganizationMembership } from "@repo/auth";
import { db } from "@repo/db";
import { project, uploadSession } from "@repo/db/schema";
import { and, eq, gt, sql } from "drizzle-orm";
import { z } from "zod";
import type { SessionFile } from "../session";
import { signUpload } from "../signing";
import { objectSessionTag } from "../store";

const detectSchema = z.object({
  projectId: z.string().min(1),
});

// One newly-completed file the caller must fire op=callback for. signature is
// HMAC(session_secret, canonical({ sessionId, file })); the secret stays here.
interface Completion {
  callbackBaseUrl: string;
  sessionId: string;
  file: { key: string; name: string; size: number; mimeType: string };
  signature: string;
}

// Atomically transition file index `idx` of `sessionId` from pending ->
// succeeded, guarded on it still being pending. The guarded UPDATE is the
// single point of idempotency: overlapping detector ticks serialize on the
// row, and only the tick that observes state = 'pending' at write time
// transitions and returns a row, so the completion (and thus onUploadComplete)
// fires exactly once per file. Returns true iff this call performed the
// transition.
async function transitionPendingToSucceeded(
  sessionId: string,
  idx: number,
): Promise<boolean> {
  const result = await db.execute(sql`
    UPDATE upload_session
    SET files = jsonb_set(files, ${`{${idx},state}`}::text[], '"succeeded"'::jsonb)
    WHERE id = ${sessionId}
      AND files -> ${idx}::int ->> 'state' = 'pending'
    RETURNING id
  `);
  return (result.rowCount ?? 0) > 0;
}

// Transition still-pending files of the caller's overdue sessions to expired.
// The CASE flips only 'pending' elements, so a 'succeeded' file is never
// downgraded; the pending-file row guard makes re-runs no-ops. ORDINALITY
// preserves file index positions, which are load-bearing elsewhere.
export async function expireOverdueSessions(
  projectId: string,
  userId: string,
): Promise<void> {
  await db.execute(sql`
    UPDATE upload_session
    SET files = (
      SELECT jsonb_agg(
        CASE WHEN elem ->> 'state' = 'pending'
             THEN jsonb_set(elem, '{state}', '"expired"')
             ELSE elem END
        ORDER BY ord
      )
      FROM jsonb_array_elements(files) WITH ORDINALITY AS t(elem, ord)
    )
    WHERE project_id = ${projectId}
      AND user_id = ${userId}
      AND expires_at <= now()
      AND files @> '[{"state":"pending"}]'::jsonb
  `);
}

// POST /api/blob/detect. The CLI dev server's detection loop calls this per
// tick; the store work lives here because the CLI never talks to the cloud
// store directly. It HEADs MinIO for each pending file of the caller's own
// sessions in the project, does the atomic idempotent pending -> succeeded
// transition, signs each completion with the session secret, and returns the
// newly-succeeded files. The CLI (not this endpoint) then POSTs each as
// op=callback: the callback target is the developer's local app, unreachable
// from a managed API.
export async function detectUploads(request: Request): Promise<Response> {
  const userId = await getSessionUserId(request.headers);
  if (!userId) {
    return Response.json({ error: "Unauthorized" }, { status: 401 });
  }

  let body: unknown;
  try {
    body = await request.json();
  } catch {
    return Response.json({ error: "Invalid request body" }, { status: 400 });
  }
  const parsed = detectSchema.safeParse(body);
  if (!parsed.success) {
    return Response.json(
      { error: "Invalid request", issues: parsed.error.issues },
      { status: 400 },
    );
  }
  const { projectId } = parsed.data;

  const [foundProject] = await db
    .select()
    .from(project)
    .where(eq(project.id, projectId));
  if (!foundProject) {
    return Response.json({ error: "Not found" }, { status: 404 });
  }
  const isMember = await verifyOrganizationMembership(
    userId,
    foundProject.organizationId,
  );
  if (!isMember) {
    return Response.json({ error: "Not found" }, { status: 404 });
  }

  // Expire overdue sessions in the same pass; completion below only sweeps live
  // sessions, so the two transitions never touch the same file.
  await expireOverdueSessions(projectId, userId);

  // Scope to the caller's own live sessions in this project: each developer's
  // `ocel dev` sweeps only its own sessions, so a completion callback is only
  // ever delivered to the callbackBaseUrl of the developer who presigned it.
  const sessions = await db
    .select()
    .from(uploadSession)
    .where(
      and(
        eq(uploadSession.projectId, projectId),
        eq(uploadSession.userId, userId),
        gt(uploadSession.expiresAt, new Date()),
      ),
    );

  const completions: Completion[] = [];
  for (const session of sessions) {
    const files = session.files as SessionFile[];
    for (let idx = 0; idx < files.length; idx++) {
      const file = files[idx];
      if (file.state !== "pending") continue;
      // Match the landed object's sessionId tag, not just its existence: with
      // randomSuffix off two sessions can share a key, and the tag is what
      // attributes the object to the session that actually produced it.
      if ((await objectSessionTag(file.key)) !== session.id) continue;

      const transitioned = await transitionPendingToSucceeded(
        session.id,
        idx,
      );
      if (!transitioned) continue;

      const signed = {
        key: file.key,
        name: file.name,
        size: file.size,
        mimeType: file.mimeType,
      };
      completions.push({
        callbackBaseUrl: session.callbackBaseUrl,
        sessionId: session.id,
        file: signed,
        signature: signUpload(session.secret, session.id, signed),
      });
    }
  }

  return Response.json({ completions }, { status: 200 });
}
