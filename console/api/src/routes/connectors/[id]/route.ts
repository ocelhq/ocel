import { db } from "@console/db";
import { connector } from "@console/db/schema";
import { eq } from "drizzle-orm";
import { patchConnectorSchema } from "../validation";
import { findOwnedConnector } from "./owned";

export async function updateConnector(request: Request, id: string): Promise<Response> {
  const owned = await findOwnedConnector(request, id);
  if (!owned.ok) {
    return owned.refusal;
  }

  let body: unknown;
  try {
    body = await request.json();
  } catch {
    return Response.json({ error: "Invalid request body" }, { status: 400 });
  }
  const parsed = patchConnectorSchema.safeParse(body);
  if (!parsed.success) {
    return Response.json(
      { error: "Invalid request", issues: parsed.error.issues },
      { status: 400 },
    );
  }

  const [held] = await db
    .update(connector)
    .set(parsed.data)
    .where(eq(connector.id, owned.connectorId))
    .returning();

  return Response.json(held, { status: 200 });
}

export async function deleteConnector(request: Request, id: string): Promise<Response> {
  const owned = await findOwnedConnector(request, id);
  if (!owned.ok) {
    return owned.refusal;
  }

  await db.delete(connector).where(eq(connector.id, owned.connectorId));
  return new Response(null, { status: 204 });
}
