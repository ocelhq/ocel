import { consoleOrigin } from "@console/auth";
import { db } from "@console/db";
import { connector } from "@console/db/schema";
import { eq, sql } from "drizzle-orm";
import { importJWK, jwtVerify } from "jose";
import { heartbeatSchema } from "../../validation";

function verifying(publicKey: string) {
  return importJWK(
    { kty: "OKP", crv: "Ed25519", x: Buffer.from(publicKey, "base64").toString("base64url") },
    "EdDSA",
  );
}

export async function connectorHeartbeat(request: Request, id: string): Promise<Response> {
  const [held] = await db
    .select({ id: connector.id, publicKey: connector.publicKey })
    .from(connector)
    .where(eq(connector.id, id));

  if (!held) {
    return Response.json({ error: "Not found" }, { status: 404 });
  }
  if (held.publicKey === null) {
    return Response.json({ error: "This connector has published no key" }, { status: 401 });
  }

  const bearer = request.headers.get("Authorization")?.match(/^Bearer (.+)$/)?.[1];
  if (!bearer) {
    return Response.json({ error: "Unauthorized" }, { status: 401 });
  }

  try {
    await jwtVerify(bearer, await verifying(held.publicKey), {
      issuer: held.id,
      subject: held.id,
      audience: consoleOrigin(),
      algorithms: ["EdDSA"],
    });
  } catch {
    return Response.json({ error: "Unauthorized" }, { status: 401 });
  }

  let body: unknown;
  try {
    body = await request.json();
  } catch {
    return Response.json({ error: "Invalid request body" }, { status: 400 });
  }
  const parsed = heartbeatSchema.safeParse(body);
  if (!parsed.success) {
    return Response.json(
      { error: "Invalid request", issues: parsed.error.issues },
      { status: 400 },
    );
  }

  const now = new Date();
  await db
    .update(connector)
    .set({
      connectedAt: sql`coalesce(${connector.connectedAt}, ${now})`,
      lastSeenAt: now,
      version: parsed.data.version,
      capabilities: parsed.data.capabilities,
    })
    .where(eq(connector.id, held.id));

  return new Response(null, { status: 204 });
}
