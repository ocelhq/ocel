import { db } from "@repo/db";
import { project } from "@repo/db/schema";
import { eq } from "drizzle-orm";
import { NextResponse } from "next/server";
import { uuidv7 } from "uuidv7";
import { getActiveOrganizationSession } from "@/lib/session";
import { createProjectSchema } from "@/lib/validation/project";

function isUniqueConstraintViolation(error: unknown): boolean {
  if (typeof error !== "object" || error === null) {
    return false;
  }
  // node-postgres reports unique violations with code 23505; drizzle wraps
  // the original pg error as `.cause`.
  if ((error as { code?: string }).code === "23505") {
    return true;
  }
  return isUniqueConstraintViolation((error as { cause?: unknown }).cause);
}

export async function GET(request: Request) {
  const session = await getActiveOrganizationSession(request.headers);
  if (!session) {
    return NextResponse.json({ error: "Unauthorized" }, { status: 401 });
  }

  const projects = await db
    .select()
    .from(project)
    .where(eq(project.organizationId, session.activeOrganizationId))
    .orderBy(project.createdAt);

  return NextResponse.json(projects, { status: 200 });
}

export async function POST(request: Request) {
  const session = await getActiveOrganizationSession(request.headers);
  if (!session) {
    return NextResponse.json({ error: "Unauthorized" }, { status: 401 });
  }

  let body: unknown;
  try {
    body = await request.json();
  } catch {
    return NextResponse.json(
      { error: "Invalid request body" },
      { status: 400 },
    );
  }
  const parsed = createProjectSchema.safeParse(body);
  if (!parsed.success) {
    return NextResponse.json(
      { error: "Invalid request", issues: parsed.error.issues },
      { status: 400 },
    );
  }

  try {
    const [created] = await db
      .insert(project)
      .values({
        id: uuidv7(),
        organizationId: session.activeOrganizationId,
        name: parsed.data.name,
        slug: parsed.data.slug,
        description: parsed.data.description ?? null,
        createdBy: session.userId,
      })
      .returning();

    return NextResponse.json(created, { status: 201 });
  } catch (error) {
    if (isUniqueConstraintViolation(error)) {
      return NextResponse.json(
        {
          error: "A project with this slug already exists in this organization",
        },
        { status: 409 },
      );
    }
    throw error;
  }
}
