import { getSessionUserId, verifyOrganizationMembership } from "@console/auth";
import { db } from "@console/db";
import { uploadSession } from "@console/db/schema";
import { eq } from "drizzle-orm";
import { z } from "zod";
import { verifyUpload } from "../signing";

const verifyUploadSchema = z.object({
  sessionId: z.string().min(1),
  signature: z.string().min(1),
  file: z.object({
    key: z.string().min(1),
    name: z.string(),
    size: z.number().int().nonnegative(),
    mimeType: z.string(),
  }),
});

export async function verifyUploadSignature(
  request: Request,
): Promise<Response> {
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
  const parsed = verifyUploadSchema.safeParse(body);
  if (!parsed.success) {
    return Response.json(
      { error: "Invalid request", issues: parsed.error.issues },
      { status: 400 },
    );
  }
  const { sessionId, signature, file } = parsed.data;

  const [row] = await db
    .select()
    .from(uploadSession)
    .where(eq(uploadSession.id, sessionId));

  if (!row || !(await verifyOrganizationMembership(userId, row.organizationId))) {
    return Response.json({ valid: false }, { status: 200 });
  }

  const valid = verifyUpload(row.secret, sessionId, file, signature);
  if (!valid) {
    return Response.json({ valid: false }, { status: 200 });
  }

  return Response.json({ valid: true, metadata: row.metadata }, { status: 200 });
}
