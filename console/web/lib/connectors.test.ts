import { db } from "@console/db";
import { connector } from "@console/db/schema";
import { eq } from "drizzle-orm";
import { beforeAll, describe, expect, it } from "vitest";
import { createTestSessionWithOrganization } from "@/test/auth-harness";
import { setupTestDatabase } from "@/test/db";
import { connectorFor, noteDenial } from "./connectors";

describe("connectorFor", () => {
  beforeAll(async () => {
    await setupTestDatabase();
  });

  it("picks the connector whose target the deploy landed in, not whichever shares the vendor", async () => {
    const session = await createTestSessionWithOrganization();
    const organizationId = session.organization.id;
    try {
      await db.insert(connector).values([
        {
          id: `first-${organizationId}`,
          organizationId,
          target: "aws/111111111111/us-east-1/main",
          vendor: "aws",
          compute: "container",
          url: "http://127.0.0.1:7777",
        },
        {
          id: `second-${organizationId}`,
          organizationId,
          target: "aws/222222222222/eu-west-1/main",
          vendor: "aws",
          compute: "serverless",
          url: "http://127.0.0.1:7778",
        },
      ]);

      const found = await connectorFor(organizationId, "aws/222222222222/eu-west-1/main");
      expect(found?.id).toBe(`second-${organizationId}`);
      expect(found?.compute).toBe("serverless");
      expect(found?.reach).toBe("dial");
    } finally {
      await session.cleanup();
    }
  });

  it("finds nothing for a target no connector claims", async () => {
    const session = await createTestSessionWithOrganization();
    try {
      expect(
        await connectorFor(session.organization.id, "aws/333333333333/us-east-1/main"),
      ).toBeNull();
      expect(await connectorFor(session.organization.id, null)).toBeNull();
    } finally {
      await session.cleanup();
    }
  });

  it("keeps a target to one connector per organization", async () => {
    const session = await createTestSessionWithOrganization();
    const organizationId = session.organization.id;
    try {
      const row = {
        organizationId,
        target: "gcp/ocel-prod/europe-west1/main",
        vendor: "gcp" as const,
        compute: "serverless" as const,
      };
      await db.insert(connector).values({ id: `one-${organizationId}`, ...row });
      await expect(
        db.insert(connector).values({ id: `two-${organizationId}`, ...row }),
      ).rejects.toThrow();
    } finally {
      await session.cleanup();
    }
  });
});

describe("noteDenial", () => {
  beforeAll(async () => {
    await setupTestDatabase();
  });

  it("writes what the connector refused, and leaves every other refusal unrecorded", async () => {
    const session = await createTestSessionWithOrganization();
    const organizationId = session.organization.id;
    const id = `denied-${organizationId}`;
    try {
      await db.insert(connector).values({
        id,
        organizationId,
        target: "aws/444444444444/us-east-1/main",
        vendor: "aws",
        url: "http://127.0.0.1:7777",
      });

      await noteDenial(id, "set", { reason: "offline", message: "nothing answered" });
      const [untouched] = await db.select().from(connector).where(eq(connector.id, id));
      expect(untouched.lastDenied).toBeNull();

      await noteDenial(id, "set", {
        reason: "denied",
        message: "the token has no envvars.write scope",
      });
      const [row] = await db.select().from(connector).where(eq(connector.id, id));
      expect(row.lastDenied?.verb).toBe("set");
      expect(row.lastDenied?.message).toBe("the token has no envvars.write scope");
      expect(Date.parse(row.lastDenied?.at ?? "")).not.toBeNaN();
    } finally {
      await session.cleanup();
    }
  });
});
