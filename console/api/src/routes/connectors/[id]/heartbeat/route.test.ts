import { generateKeyPairSync, type KeyObject } from "node:crypto";
import { db } from "@console/db";
import { connector } from "@console/db/schema";
import { eq } from "drizzle-orm";
import { SignJWT } from "jose";
import { beforeAll, describe, expect, it } from "vitest";
import { createTestSessionWithOrganization } from "../../../../../test/auth-harness";
import { setupTestDatabase } from "../../../../../test/db";
import { upsertConnector } from "../../route";
import { connectorHeartbeat } from "./route";

const origin = "http://localhost:3000";

function keyPair() {
  const { publicKey, privateKey } = generateKeyPairSync("ed25519");
  const { x } = publicKey.export({ format: "jwk" }) as { x: string };
  return { privateKey, publicKey: Buffer.from(x, "base64url").toString("base64") };
}

async function bearer(privateKey: KeyObject, id: string, audience = origin) {
  return new SignJWT({})
    .setProtectedHeader({ alg: "EdDSA" })
    .setIssuer(id)
    .setSubject(id)
    .setAudience(audience)
    .setIssuedAt()
    .setExpirationTime("5m")
    .sign(privateKey);
}

function beat(
  token: string | null,
  body: unknown = { version: "0.0.0-alpha", capabilities: ["envvars.read"] },
) {
  return new Request("http://localhost/api/connectors/x/heartbeat", {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      ...(token === null ? {} : { Authorization: `Bearer ${token}` }),
    },
    body: JSON.stringify(body),
  });
}

async function paired() {
  const session = await createTestSessionWithOrganization();
  const keys = keyPair();
  const held = await (
    await upsertConnector(
      new Request("http://localhost/api/connectors", {
        method: "PUT",
        headers: { ...Object.fromEntries(session.headers), "Content-Type": "application/json" },
        body: JSON.stringify({
          target: "vps/SHA256:abc/ocel",
          vendor: "vps",
        }),
      }),
    )
  ).json();
  await db.update(connector).set({ publicKey: keys.publicKey }).where(eq(connector.id, held.id));
  return { session, keys, id: held.id as string };
}

describe("POST /api/connectors/{id}/heartbeat", () => {
  beforeAll(async () => {
    process.env.BETTER_AUTH_URL = origin;
    await setupTestDatabase();
  });

  it("answers 204 and moves the liveness columns", async () => {
    const { session, keys, id } = await paired();

    try {
      const response = await connectorHeartbeat(beat(await bearer(keys.privateKey, id)), id);
      expect(response.status).toBe(204);

      const [row] = await db.select().from(connector).where(eq(connector.id, id));
      expect(row.connectedAt).not.toBeNull();
      expect(row.lastSeenAt).not.toBeNull();
      expect(row.version).toBe("0.0.0-alpha");
      expect(row.capabilities).toEqual(["envvars.read"]);

      const first = row.connectedAt;
      await connectorHeartbeat(beat(await bearer(keys.privateKey, id)), id);
      const [again] = await db.select().from(connector).where(eq(connector.id, id));
      expect(again.connectedAt?.getTime()).toBe(first?.getTime());
      expect(again.lastSeenAt?.getTime()).toBeGreaterThanOrEqual(row.lastSeenAt?.getTime() ?? 0);
    } finally {
      await session.cleanup();
    }
  });

  it("answers 401 to a signature from another key", async () => {
    const { session, id } = await paired();

    try {
      const stranger = keyPair();
      const response = await connectorHeartbeat(beat(await bearer(stranger.privateKey, id)), id);
      expect(response.status).toBe(401);

      const [row] = await db.select().from(connector).where(eq(connector.id, id));
      expect(row.lastSeenAt).toBeNull();
    } finally {
      await session.cleanup();
    }
  });

  it("answers 401 to a token minted for another audience or issuer", async () => {
    const { session, keys, id } = await paired();

    try {
      const elsewhere = await bearer(keys.privateKey, id, "https://elsewhere.test");
      expect((await connectorHeartbeat(beat(elsewhere), id)).status).toBe(401);

      const stranger = await bearer(keys.privateKey, "someone-else");
      expect((await connectorHeartbeat(beat(stranger), id)).status).toBe(401);
    } finally {
      await session.cleanup();
    }
  });

  it("answers 401 when no bearer is carried", async () => {
    const { session, id } = await paired();

    try {
      expect((await connectorHeartbeat(beat(null), id)).status).toBe(401);
    } finally {
      await session.cleanup();
    }
  });

  it("answers 404 for an id the console does not hold", async () => {
    const keys = keyPair();
    const response = await connectorHeartbeat(
      beat(await bearer(keys.privateKey, "nobody")),
      "nobody",
    );
    expect(response.status).toBe(404);
  });

  it("refuses a body naming no version", async () => {
    const { session, keys, id } = await paired();

    try {
      const token = await bearer(keys.privateKey, id);
      const response = await connectorHeartbeat(beat(token, { capabilities: [] }), id);
      expect(response.status).toBe(400);
    } finally {
      await session.cleanup();
    }
  });
});
