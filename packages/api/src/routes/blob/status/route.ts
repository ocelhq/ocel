import { getSessionUserId } from "@repo/auth";
import { db } from "@repo/db";
import { uploadSession } from "@repo/db/schema";
import { eq } from "drizzle-orm";
import { aggregateState, type SessionFile } from "../session";

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

  if (!row || row.userId !== userId) {
    return Response.json({ error: "Not found" }, { status: 404 });
  }

  const state = aggregateState(row.files as SessionFile[]);
  const error = state === "expired" ? "upload expired" : undefined;
  return Response.json({ state, error }, { status: 200 });
}
