import { beforeAll, describe, expect, it } from "vitest";
import { createTestSessionWithOrganization } from "./auth-harness";
import { setupTestDatabase } from "./db";

describe("test harness smoke test", () => {
  beforeAll(async () => {
    await setupTestDatabase();
  });

  it("connects to the test database and mints a real session + organization", async () => {
    const session = await createTestSessionWithOrganization();

    try {
      expect(session.user.id).toBeTruthy();
      expect(session.token).toBeTruthy();
      expect(session.organization.id).toBeTruthy();
      expect(session.organization.members[0]?.userId).toBe(session.user.id);
    } finally {
      await session.cleanup();
    }
  });
});
