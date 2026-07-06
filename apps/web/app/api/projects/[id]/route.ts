import { eq } from "drizzle-orm";
import { NextResponse } from "next/server";
import { project } from "@/db/schema";
import { db } from "@/lib/db";
import { getSessionUserId, verifyOrganizationMembership } from "@/lib/session";

export async function GET(
  request: Request,
  { params }: { params: Promise<{ id: string }> },
) {
  const userId = await getSessionUserId(request.headers);
  if (!userId) {
    return NextResponse.json({ error: "Unauthorized" }, { status: 401 });
  }

  const { id } = await params;

  const [found] = await db.select().from(project).where(eq(project.id, id));

  // Same 404 whether the Project doesn't exist or the caller isn't a member
  // of its org - don't leak existence to non-members.
  if (!found) {
    return NextResponse.json({ error: "Not found" }, { status: 404 });
  }

  const isMember = await verifyOrganizationMembership(
    userId,
    found.organizationId,
  );
  if (!isMember) {
    return NextResponse.json({ error: "Not found" }, { status: 404 });
  }

  return NextResponse.json(found, { status: 200 });
}
