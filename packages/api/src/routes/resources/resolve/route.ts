import { getSessionUserId, verifyOrganizationMembership } from "@repo/auth";
import { db } from "@repo/db";
import { project, resourceAssignment } from "@repo/db/schema";
import { and, eq } from "drizzle-orm";
import { uuidv7 } from "uuidv7";
import { resourceTypeRegistry } from "./registry";
import { resolveResourcesSchema } from "./validation";

// Drives the CLI's resolve cache (ocelhq-amu.5): responses are safe to reuse
// until this TTL elapses.
const RESOLVE_TTL_MS = 60 * 60 * 1000;

function buildResourceEnvKey(type: string, name: string): string {
  return `OCEL_RESOURCE_${type}_${name}`;
}

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

export async function resolveResources(request: Request): Promise<Response> {
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
  const parsed = resolveResourcesSchema.safeParse(body);
  if (!parsed.success) {
    return Response.json(
      { error: "Invalid request", issues: parsed.error.issues },
      { status: 400 },
    );
  }
  const { projectId, resources } = parsed.data;

  const [foundProject] = await db
    .select()
    .from(project)
    .where(eq(project.id, projectId));

  // Same 404 whether the Project doesn't exist or the caller isn't a member
  // of its org - matches getProjectById's convention of not leaking
  // existence to non-members.
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

  const env: Record<string, string> = {};

  for (const resource of resources) {
    const handler = resourceTypeRegistry[resource.type];
    if (!handler) {
      return Response.json(
        { error: `Unsupported resource type "${resource.type}"` },
        { status: 400 },
      );
    }

    const reuseKey = and(
      eq(resourceAssignment.userId, userId),
      eq(resourceAssignment.projectId, projectId),
      eq(resourceAssignment.resourceName, resource.name),
      eq(resourceAssignment.resourceType, resource.type),
    );

    let [assignment] = await db
      .select()
      .from(resourceAssignment)
      .where(reuseKey);

    if (!assignment) {
      const provisioned = await handler.provision({
        userId,
        projectId,
        resourceName: resource.name,
        resourceType: resource.type,
      });

      try {
        [assignment] = await db
          .insert(resourceAssignment)
          .values({
            id: uuidv7(),
            userId,
            projectId,
            resourceName: resource.name,
            resourceType: resource.type,
            config: resource.config,
            databaseName: provisioned.databaseName,
            roleName: provisioned.roleName,
            password: provisioned.password,
          })
          .returning();
      } catch (error) {
        if (!isUniqueConstraintViolation(error)) {
          throw error;
        }
        // Lost a race with a concurrent resolve for the same reuse key -
        // the role/db we just provisioned are deterministically named from
        // identity, so they're identical to the winner's; the persisted
        // row is the source of truth, so defer to it instead of erroring.
        [assignment] = await db
          .select()
          .from(resourceAssignment)
          .where(reuseKey);
      }
    }

    if (!assignment) {
      throw new Error(
        `Assignment for resource "${resource.name}" was provisioned but could not be found or persisted`,
      );
    }

    const connectionString = handler.buildConnectionString(assignment);
    env[buildResourceEnvKey(resource.type, resource.name)] = JSON.stringify({
      connectionString,
    });
  }

  return Response.json(
    { env, expiresAt: new Date(Date.now() + RESOLVE_TTL_MS).toISOString() },
    { status: 200 },
  );
}
