import { getActiveOrganizationSession } from "@console/auth";
import { db } from "@console/db";
import { connector } from "@console/db/schema";
import { eq } from "drizzle-orm";
import { uuidv7 } from "uuidv7";
import { upsertConnectorSchema } from "./validation";

export async function listConnectors(request: Request): Promise<Response> {
  const session = await getActiveOrganizationSession(request.headers);
  if (!session) {
    return Response.json({ error: "Unauthorized" }, { status: 401 });
  }

  const rows = await db
    .select()
    .from(connector)
    .where(eq(connector.organizationId, session.activeOrganizationId))
    .orderBy(connector.createdAt);

  return Response.json(rows, { status: 200 });
}

export async function upsertConnector(request: Request): Promise<Response> {
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
  const parsed = upsertConnectorSchema.safeParse(body);
  if (!parsed.success) {
    return Response.json(
      { error: "Invalid request", issues: parsed.error.issues },
      { status: 400 },
    );
  }

  const [held] = await db
    .insert(connector)
    .values({
      id: uuidv7(),
      organizationId: session.activeOrganizationId,
      target: parsed.data.target,
      vendor: parsed.data.vendor,
      reach: parsed.data.reach,
    })
    .onConflictDoUpdate({
      target: [connector.organizationId, connector.target],
      set: {
        vendor: parsed.data.vendor,
        reach: parsed.data.reach,
      },
    })
    .returning();

  return Response.json(held, { status: 200 });
}
