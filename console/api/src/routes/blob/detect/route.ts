import { getSessionUserId, verifyOrganizationMembership } from "@console/auth";
import { db } from "@console/db";
import { project, uploadSession } from "@console/db/schema";
import { and, eq, gt, sql } from "drizzle-orm";
import { z } from "zod";
import type { SessionFile } from "../session";
import { signUpload } from "../signing";
import { objectSessionTag } from "../store";

const detectSchema = z.object({
  projectId: z.string().min(1),
});

interface Completion {
  callbackBaseUrl: string;
  sessionId: string;
  file: { key: string; name: string; size: number; mimeType: string };
  signature: string;
}

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

  await expireOverdueSessions(projectId, userId);

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
