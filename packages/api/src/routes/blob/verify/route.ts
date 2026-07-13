import { getSessionUserId, verifyOrganizationMembership } from "@repo/auth";
import { db } from "@repo/db";
import { uploadSession } from "@repo/db/schema";
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

// POST /api/blob/verify. Backs buckets.v1.BucketService.VerifyUploadSignature:
// re-derives the per-session HMAC over the completion callback and constant-time
// compares, returning the stored metadata verbatim on success. The secret never
// leaves here. An unknown session or bad signature returns { valid:false } as a
// 200, not an error - the RPC is a boolean verdict, not a lookup.
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

  // Unknown session, or a caller who isn't a member of its org, both fall
  // through to a plain valid:false - the verdict never leaks whether the
  // session exists, and only a member's callback can be verified.
  if (!row || !(await verifyOrganizationMembership(userId, row.organizationId))) {
    return Response.json({ valid: false }, { status: 200 });
  }

  const valid = verifyUpload(row.secret, sessionId, file, signature);
  if (!valid) {
    return Response.json({ valid: false }, { status: 200 });
  }

  // metadata is stored base64 verbatim; returned as-is so the shim decodes it
  // straight into the proto's bytes field.
  return Response.json({ valid: true, metadata: row.metadata }, { status: 200 });
}
