import { getSessionUserId, verifyOrganizationMembership } from "@repo/auth";
import { db } from "@repo/db";
import { project, uploadSession } from "@repo/db/schema";
import { and, eq, gt, sql } from "drizzle-orm";
import { z } from "zod";
import type { SessionFile } from "../session";
import { signUpload } from "../signing";
import { objectExists } from "../store";

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

// POST /api/blob/detect. The CLI dev server's detection loop calls this per
// tick with its projectId; the loop cannot touch the store itself (the CLI
// never talks to the cloud store directly), so the store work lives here. This
// endpoint owns the detector half of the completion architecture: it HEADs
// MinIO for each pending file of the caller's own sessions in the project,
// performs the atomic idempotent pending -> succeeded transition, signs the
// completion with the session secret, and returns the newly-succeeded files.
// The caller then POSTs each as op=callback to its callbackBaseUrl (which in
// real `ocel dev` is the app on the developer's local machine, unreachable
// from a managed API - hence the CLI, not this endpoint, delivers the
// callback).
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
      if (!(await objectExists(file.key))) continue;

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
