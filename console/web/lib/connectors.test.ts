import { db } from "@console/db";
import { connector } from "@console/db/schema";
import { beforeAll, describe, expect, it } from "vitest";
import { createTestSessionWithOrganization } from "@/test/auth-harness";
import { setupTestDatabase } from "@/test/db";
import { connectorFor } from "./connectors";

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
