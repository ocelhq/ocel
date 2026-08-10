import { randomBytes } from "node:crypto";
import { getSessionUserId, verifyOrganizationMembership } from "@console/auth";
import { db } from "@console/db";
import { project, uploadSession } from "@console/db/schema";
import { eq } from "drizzle-orm";
import { uuidv7 } from "uuidv7";
import type { SessionFile } from "../session";
import { presignPut } from "../store";
import { presignUploadSchema } from "./validation";

const SESSION_TTL_MS = 2 * 60 * 60 * 1000;

export async function presignUpload(request: Request): Promise<Response> {
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
  const parsed = presignUploadSchema.safeParse(body);
  if (!parsed.success) {
    return Response.json(
      { error: "Invalid request", issues: parsed.error.issues },
      { status: 400 },
    );
  }
  const { projectId, bucket, files, metadata, contentDisposition, callbackBaseUrl } =
    parsed.data;

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

  const organizationId = foundProject.organizationId;
  const prefix = `${organizationId}/${projectId}/${userId}/`;

  const sessionId = uuidv7();
  const secret = randomBytes(32).toString("base64url");

  const targets: {
    url: string;
    key: string;
    name: string;
    contentDisposition?: string;
  }[] = [];
  const fileStates: SessionFile[] = [];
  for (const file of files) {
    const key = prefix + file.key;
    const url = await presignPut({
      key,
      contentType: file.mimeType,
      contentLength: file.size,
      sessionId,
      contentDisposition,
    });
    targets.push({
      url,
      key,
      name: file.name,
      contentDisposition: contentDisposition || undefined,
    });
    fileStates.push({
      key,
      name: file.name,
      size: file.size,
      mimeType: file.mimeType,
      state: "pending",
    });
  }

  await db.insert(uploadSession).values({
    id: sessionId,
    organizationId,
    projectId,
    userId,
    bucket,
    secret,
    callbackBaseUrl,
    contentDisposition,
    metadata,
    files: fileStates,
    expiresAt: new Date(Date.now() + SESSION_TTL_MS),
  });

  return Response.json({ sessionId, files: targets }, { status: 200 });
}
