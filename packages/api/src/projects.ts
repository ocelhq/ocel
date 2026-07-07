import { getActiveOrganizationSession } from "@repo/auth";
import { db } from "@repo/db";
import { project } from "@repo/db/schema";
import { eq } from "drizzle-orm";
import { uuidv7 } from "uuidv7";
import { createProjectSchema } from "./validation/project";

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

export async function listProjects(request: Request): Promise<Response> {
  const session = await getActiveOrganizationSession(request.headers);
  if (!session) {
    return Response.json({ error: "Unauthorized" }, { status: 401 });
  }

  const projects = await db
    .select()
    .from(project)
    .where(eq(project.organizationId, session.activeOrganizationId))
    .orderBy(project.createdAt);

  return Response.json(projects, { status: 200 });
}

export async function createProject(request: Request): Promise<Response> {
  const session = await getActiveOrganizationSession(request.headers);
  if (!session) {
    return Response.json({ error: "Unauthorized" }, { status: 401 });
  }

  let body: unknown;
  try {
    body = await request.json();
  } catch {
    return Response.json({ error: "Invalid request body" }, { status: 400 });
  }
  const parsed = createProjectSchema.safeParse(body);
  if (!parsed.success) {
    return Response.json(
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

    return Response.json(created, { status: 201 });
  } catch (error) {
    if (isUniqueConstraintViolation(error)) {
      return Response.json(
        {
          error: "A project with this slug already exists in this organization",
        },
        { status: 409 },
      );
    }
    throw error;
  }
}
